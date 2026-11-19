package app

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"flow/internal/harness"
	"flow/internal/harness/claude"

	"golang.org/x/mod/semver"
)

// embeddedSkillFS holds the entire skill directory — SKILL.md (the lean
// resident core loaded by the Skill tool) plus references/*.md (rarely-
// needed workflows loaded on demand). Embedding the whole tree lets the
// installer lay the reference files down alongside SKILL.md so the core
// can point at them.
//
//go:embed skill
var embeddedSkillFS embed.FS

// skillFiles returns the embedded skill tree rooted at the skill/
// directory, so walking it yields "SKILL.md" and "references/<x>.md"
// (not "skill/SKILL.md"). Passed to Harness.InstallSkill.
func skillFiles() fs.FS {
	sub, err := fs.Sub(embeddedSkillFS, "skill")
	if err != nil {
		// The embed directive guarantees skill/ exists; this is
		// unreachable in a correctly-built binary.
		return embeddedSkillFS
	}
	return sub
}

// hookCommand is the exact string the harness's settings.json
// (settings.json / hooks.json depending on harness) records as the
// SessionStart hook handler. Stable — changing this string would
// orphan existing installations.
const hookCommand = "flow hook session-start"

// userPromptSubmitHookCommand is the exact string settings.json records
// as the UserPromptSubmit hook handler. In bound sessions the command
// injects a tiny drift/close-out anchor; unbound sessions no-op. Stable
// — changing it would orphan existing installations.
const userPromptSubmitHookCommand = "flow hook user-prompt-submit"

// stopHookCommand is the exact string settings.json records for the
// bus post-nudge at turn end. Stable — changing it would orphan
// existing installations.
const stopHookCommand = "flow hook stop"

// preToolUseHookCommand is the exact string settings.json records for
// the delta-gated pre-tool-call inbox nudge. In bound sessions it
// surfaces a just-arrived directed message before the tool runs; unbound
// sessions and quiet inboxes no-op. Stable — changing it would orphan
// existing installations.
const preToolUseHookCommand = "flow hook pre-tool-use"

// postToolUseHookCommand is RETIRED (per-tool-call delivery replaced by
// the `flow inbox pop --wait` listener discipline). The string is kept
// only so installs that registered it can be cleaned up on upgrade.
const postToolUseHookCommand = "flow hook post-tool-use"

// readSkillVersion returns the version string recorded in h's
// skill-version sidecar, or "" if missing/unreadable. Each harness has
// its own sidecar (SkillVersionPath), so versions are tracked per harness.
func readSkillVersion(h harness.Harness) string {
	p, err := h.SkillVersionPath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// writeSkillVersion records `v` in h's sidecar as the version of the
// binary that installed its current skill content. Errors are
// non-fatal — failing to write the sidecar should never block a
// successful skill install.
func writeSkillVersion(h harness.Harness, v string) error {
	p, err := h.SkillVersionPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(v+"\n"), 0o644)
}

// harnessFlag registers the shared --harness flag on install-style
// commands. Empty = every applicable harness.
func harnessFlag(fs *flag.FlagSet) *string {
	return fs.String("harness", "", "limit to one harness ("+registeredHarnessNames()+")")
}

// resolveTargets turns the --harness value into the harnesses to act
// on, printing a usage error (caller returns 2) for an unknown name.
func resolveTargets(name string, fallback func() []harness.Harness) ([]harness.Harness, bool) {
	hs, err := targetHarnesses(name, fallback)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return nil, false
	}
	return hs, true
}

// flowHook is one live hook flow registers in every harness.
type flowHook struct {
	event   string
	command string
	desc    string
	install func(harness.Harness, string) (bool, error)
	remove  func(harness.Harness, string) (bool, error)
}

// flowHooks is the full live hook set. `flow init`, `flow skill
// install|update`, and auto-upgrade all install exactly this set, and
// uninstall removes it, so the commands can't drift apart.
func flowHooks() []flowHook {
	return []flowHook{
		{"SessionStart", hookCommand, "fires on startup + resume",
			harness.Harness.InstallSessionStartHook, harness.Harness.UninstallSessionStartHook},
		{"UserPromptSubmit", userPromptSubmitHookCommand, "nudges drift/close-out on bound-session prompts",
			harness.Harness.InstallUserPromptSubmitHook, harness.Harness.UninstallUserPromptSubmitHook},
		{"Stop", stopHookCommand, "page-bus post nudge at turn end",
			harness.Harness.InstallStopHook, harness.Harness.UninstallStopHook},
		{"PreToolUse", preToolUseHookCommand, "delta-gated inbox nudge before each tool call",
			harness.Harness.InstallPreToolUseHook, harness.Harness.UninstallPreToolUseHook},
	}
}

// pluginCommand is cmd as the Claude flow plugin's hooks.json runs it:
// prefixed with FLOW_HOOK_SRC=plugin so the handler can tell a plugin
// invocation from a legacy settings.json one (see hookguard.go). An env
// prefix, not a flag: an older flow binary (a rollback) ignores it, where
// an unknown flag would exit 2 — which Claude treats as "block this tool
// call / prompt / stop". The bare strings above stay the exact match keys
// for stripping old settings entries.
func pluginCommand(cmd string) string { return hookSrcEnv + "=" + hookSrcPlugin + " " + cmd }

// pluginHooks is the hook set the Claude plugin's hooks.json carries:
// every flowHooks entry plus the asyncRewake waiter on SessionStart and
// Stop, each through pluginCommand. Generated from flowHooks so the plugin
// and the legacy settings install can't drift apart.
func pluginHooks() []harness.PluginHook {
	var out []harness.PluginHook
	for _, fh := range flowHooks() {
		out = append(out, harness.PluginHook{Event: fh.event, Command: pluginCommand(fh.command)})
	}
	for _, ev := range []string{"SessionStart", "Stop"} {
		out = append(out, harness.PluginHook{Event: ev, Command: pluginCommand(rewakeHookCommand),
			Rewake: true, RewakeMessage: rewakeMessage})
	}
	return out
}

// installHooks registers every live flow hook for h. Hook failures are
// warnings, not errors: the skill is usable without hooks and the user
// can wire them manually. quiet suppresses per-hook stdout (auto-upgrade).
//
// A plugin-capable harness (Claude) gets the hooks as plugin files inside
// its skill dir and its user settings are never written. Other harnesses
// (Codex, prx) get the settings-file merge, plus removal of the retired
// PostToolUse hook.
func installHooks(h harness.Harness, quiet bool) {
	installHooksOpts(h, quiet, false)
}

// installHooksOpts is installHooks; stripLegacy forces the Claude legacy
// settings.json strip (`--strip-legacy`).
func installHooksOpts(h harness.Harness, quiet, stripLegacy bool) {
	name := h.Name()
	if ph, ok := h.(harness.PluginHookInstaller); ok {
		changed, err := ph.InstallPluginHooks(Version, pluginHooks())
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "warning: %s: could not install the flow plugin hooks: %v\n", name, err)
		case quiet:
		case changed:
			fmt.Printf("%s: installed flow plugin hooks (SessionStart, UserPromptSubmit, Stop, PreToolUse, wake) — no settings.json changes\n", name)
		default:
			fmt.Printf("%s: flow plugin hooks already installed — leaving as is\n", name)
		}
		if err == nil && !quiet && ph.PluginDisabled() {
			fmt.Printf("%s: note: the flow plugin is disabled in your settings (enabledPlugins %q is false), so its hooks will not run; flow leaves that choice alone\n", name, claude.PluginID)
		}
		// Strip the legacy settings.json copies once that is safe (or
		// right away with --strip-legacy); see legacystrip.go.
		// A strip announces itself (stderr + a human FYI).
		if stripped, why := stripLegacyClaudeHooks(stripLegacy, quiet); !stripped && !quiet && legacyClaudeEntriesPresent() {
			fmt.Printf("%s: kept flow's legacy hooks in ~/.claude/settings.json for now: %s\n", name, why)
		}
		return
	}
	if removed, err := h.UninstallPostToolUseHook(postToolUseHookCommand); err == nil && removed && !quiet {
		fmt.Printf("%s: removed retired PostToolUse hook (bus delivery now rides `flow inbox pop --wait`)\n", name)
	}
	for _, fh := range flowHooks() {
		added, err := fh.install(h, fh.command)
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "warning: %s: could not install %s hook: %v\n", name, fh.event, err)
		case quiet:
		case added:
			fmt.Printf("%s: installed %s hook (%s)\n", name, fh.event, fh.desc)
		default:
			fmt.Printf("%s: %s hook already installed — leaving as is\n", name, fh.event)
		}
	}
	// Harness wake fallback (asyncRewake waiter) for a non-plugin harness
	// that supports it. Installed even when wake is off for this shell:
	// the waiter checks the switch itself.
	if rw, ok := h.(harness.RewakeHookInstaller); ok {
		added, err := rw.InstallRewakeHook(rewakeHookCommand, rewakeMessage)
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "warning: %s: could not install the wake fallback hook: %v\n", name, err)
		case added && !quiet:
			fmt.Printf("%s: installed wake fallback hook (asyncRewake on SessionStart + Stop)\n", name)
		}
	}
}

// migrateLegacyHooks strips every hook entry flow ever wrote into h's
// settings by exact command match — the live set, the retired PostToolUse
// hook and the rewake waiter — leaving unrelated hooks alone. Used by
// uninstall and by the Claude legacy strip (legacystrip.go).
// Returns how many hook kinds were removed.
func migrateLegacyHooks(h harness.Harness, quiet bool) int {
	name := h.Name()
	n := 0
	hooks := append(flowHooks(), flowHook{"PostToolUse", postToolUseHookCommand, "", nil,
		harness.Harness.UninstallPostToolUseHook})
	for _, fh := range hooks {
		if removed, err := fh.remove(h, fh.command); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: could not remove %s hook: %v\n", name, fh.event, err)
		} else if removed {
			n++
			if !quiet {
				fmt.Printf("%s: removed %s hook\n", name, fh.event)
			}
		}
	}
	if rw, ok := h.(harness.RewakeHookInstaller); ok {
		if removed, err := rw.UninstallRewakeHook(rewakeHookCommand); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: could not remove the wake fallback hook: %v\n", name, err)
		} else if removed {
			n++
			if !quiet {
				fmt.Printf("%s: removed wake fallback hook\n", name)
			}
		}
	}
	return n
}

// skillVersionKey reduces a version to the release it was built from
// (a `git describe` source build compares as its tag) and reports
// whether it is a comparable semver. "dev", "" and anything else
// unparsable are not.
func skillVersionKey(v string) (string, bool) {
	v = describeSuffixRe.ReplaceAllString(strings.TrimSpace(v), "")
	return v, semver.IsValid(v)
}

// skillRefreshWanted reports whether a binary of version `binary` may
// implicitly replace a skill + hooks recorded as `installed`. It is
// true only when the binary is strictly newer: an implicit refresh
// must never downgrade. An older or other binary (a `.bak` copy, a
// rollback, a repro of an old release) run with the real HOME would
// otherwise overwrite the newer hooks with its own.
//
//   - binary "dev"/unparsable  → false (a developer's build never auto-installs)
//   - installed ""             → true  (pre-sidecar install: historical refresh)
//   - installed "dev"/unparsable → false (a developer's install; hands off)
//   - otherwise                → semver(binary) > semver(installed)
func skillRefreshWanted(installed, binary string) bool {
	b, ok := skillVersionKey(binary)
	if !ok {
		return false
	}
	if strings.TrimSpace(installed) == "" {
		return true
	}
	i, ok := skillVersionKey(installed)
	if !ok {
		return false
	}
	return semver.Compare(b, i) > 0
}

// installedIsNewer reports whether the skill recorded as `installed`
// is newer than (or a dev build replaced by) a `binary` release — the
// case where installing would be a downgrade.
func installedIsNewer(installed, binary string) bool {
	installed = strings.TrimSpace(installed)
	if installed == "" || installed == binary {
		return false
	}
	if _, ok := skillVersionKey(binary); !ok {
		return false // a dev binary: the user's own build, no warning
	}
	i, ok := skillVersionKey(installed)
	if !ok {
		return true // a dev/source install replaced by a release
	}
	b, _ := skillVersionKey(binary)
	return semver.Compare(i, b) > 0
}

// maybeAutoUpgradeSkill refreshes each harness's skill + hooks when the
// running binary is strictly newer than the version recorded in that
// harness's sidecar. It runs on every flow invocation so a user who
// replaced the binary gets a self-healing upgrade.
//
// The check is intentionally conservative — per harness, it does
// nothing when:
//   - FLOW_NO_AUTO_UPGRADE is set. It stops every implicit upgrade:
//     the binary self-upgrade and this skill + hook refresh.
//   - The skill isn't installed for that harness (SKILL.md missing).
//     Treat this as an explicit per-harness opt-out; never re-install.
//   - The binary is not strictly newer than the installed skill — see
//     skillRefreshWanted. It never downgrades, never touches a dev
//     install, and a dev binary never refreshes.
//
// All errors are silent — auto-upgrade is best-effort plumbing, not a
// command. A user-visible failure here would be far more annoying
// than the eventual symptom of a stale skill.
func maybeAutoUpgradeSkill() {
	if os.Getenv(selfUpgradeEnv) != "" {
		return
	}
	if _, ok := skillVersionKey(Version); !ok {
		return
	}
	for _, h := range allHarnesses() {
		if !skillInstalled(h) {
			continue // opted out; don't reinstall behind their back
		}
		if !skillRefreshWanted(readSkillVersion(h), Version) {
			continue
		}
		if err := h.InstallSkill(skillFiles()); err != nil {
			continue
		}
		_ = writeSkillVersion(h, Version)
		installHooks(h, true)
		fmt.Fprintf(os.Stderr, "flow: upgraded %s skill to %s\n", h.Name(), Version)
	}
}

// cmdSkill dispatches `flow skill install|uninstall|update`.
func cmdSkill(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: skill requires a subcommand (install|uninstall|update)")
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "install":
		return skillInstall(rest, false)
	case "update":
		return skillInstall(rest, true)
	case "uninstall":
		return skillUninstall(rest)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown skill subcommand %q\n", sub)
		return 2
	}
}

// skillInstall installs the skill + hooks for every present harness
// (or just --harness). A harness whose skill already exists without
// --force is reported and skipped; the others still install. rc=1 if
// any harness failed.
func skillInstall(args []string, forceDefault bool) int {
	fs := flagSet("skill install")
	force := fs.Bool("force", forceDefault, "overwrite an existing installation")
	skipHook := fs.Bool("skip-hook", false, "don't auto-install the hooks")
	stripLegacy := fs.Bool("strip-legacy", false, "claude: remove flow's legacy hooks from ~/.claude/settings.json now, even if a live session still uses them")
	only := harnessFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	targets, ok := resolveTargets(*only, presentHarnesses)
	if !ok {
		return 2
	}
	rc := 0
	for _, h := range targets {
		if !installSkillFor(h, *force, *skipHook, *stripLegacy) {
			rc = 1
		}
	}
	return rc
}

func installSkillFor(h harness.Harness, force, skipHook, stripLegacy bool) bool {
	name := h.Name()
	dest, err := h.SkillInstallPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s: %v\n", name, err)
		return false
	}
	if _, err := os.Stat(dest); err == nil && !force {
		fmt.Fprintf(os.Stderr, "error: %s: %s already exists; use --force to overwrite\n", name, dest)
		return false
	} else if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error: %s: stat %s: %v\n", name, dest, err)
		return false
	}
	if prev := readSkillVersion(h); installedIsNewer(prev, Version) {
		fmt.Fprintf(os.Stderr, "warning: %s: replacing the installed skill + hooks from %s (newer) with this older flow %s\n", name, prev, Version)
	}
	if err := h.InstallSkill(skillFiles()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s: %v\n", name, err)
		return false
	}
	if err := writeSkillVersion(h, Version); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s: could not record skill version: %v\n", name, err)
	}
	fmt.Printf("%s: installed flow skill to %s\n", name, dest)
	if skipHook {
		fmt.Printf("%s: --skip-hook: leaving harness settings alone\n", name)
		return true
	}
	installHooksOpts(h, false, stripLegacy)
	return true
}

// installedHarnesses returns every harness that has the flow skill
// installed, falling back to defaultHarness() so uninstall still cleans
// that harness's hooks when the skill dir is already gone.
func installedHarnesses() []harness.Harness {
	var out []harness.Harness
	for _, h := range allHarnesses() {
		if p, err := h.SkillInstallPath(); err == nil {
			if _, err := os.Stat(filepath.Dir(p)); err == nil {
				out = append(out, h)
			}
		}
	}
	if len(out) == 0 {
		out = []harness.Harness{defaultHarness()}
	}
	return out
}

// skillUninstall removes the skill + hooks from every harness where the
// skill is installed (or just --harness).
func skillUninstall(args []string) int {
	fs := flagSet("skill uninstall")
	keepHook := fs.Bool("keep-hook", false, "don't remove the hooks")
	only := harnessFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	targets, ok := resolveTargets(*only, installedHarnesses)
	if !ok {
		return 2
	}
	rc := 0
	for _, h := range targets {
		if !uninstallSkillFor(h, *keepHook) {
			rc = 1
		}
	}
	return rc
}

func uninstallSkillFor(h harness.Harness, keepHook bool) bool {
	name := h.Name()
	dest, err := h.SkillInstallPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s: %v\n", name, err)
		return false
	}
	skillDir := filepath.Dir(dest)
	ph, plugin := h.(harness.PluginHookInstaller)
	hadPlugin := plugin && ph.PluginInstalled()
	if _, err := os.Stat(skillDir); os.IsNotExist(err) {
		fmt.Printf("%s: flow skill not installed at %s — nothing to do\n", name, skillDir)
	} else if keepHook && plugin {
		// The plugin's hooks live in the skill dir: keep exactly those.
		if err := ph.RemoveSkillKeepPlugin(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", name, err)
			return false
		}
		fmt.Printf("%s: uninstalled flow skill from %s (kept the plugin hooks)\n", name, skillDir)
	} else {
		// For a plugin harness this removes the plugin manifest + hooks too.
		if err := h.UninstallSkill(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", name, err)
			return false
		}
		fmt.Printf("%s: uninstalled flow skill from %s\n", name, skillDir)
		if hadPlugin {
			fmt.Printf("%s: removed flow plugin hooks\n", name)
		}
	}
	if keepHook {
		fmt.Printf("%s: --keep-hook: leaving hooks in place\n", name)
		return true
	}
	// Legacy settings entries (pre-plugin installs) are stripped too.
	migrateLegacyHooks(h, false)
	return true
}
