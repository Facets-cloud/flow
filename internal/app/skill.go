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

// installHooks registers every live flow hook for h and removes the
// retired PostToolUse hook. Hook failures are warnings, not errors: the
// skill is usable without hooks and the user can wire them manually.
// quiet suppresses per-hook stdout (auto-upgrade).
func installHooks(h harness.Harness, quiet bool) {
	name := h.Name()
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
}

// maybeAutoUpgradeSkill checks each harness's recorded skill version
// against the running binary's version and, where they differ,
// refreshes that harness's skill + hooks. Designed to run on every flow
// invocation so the user gets a self-healing upgrade flow after
// replacing the binary.
//
// The check is intentionally conservative — per harness, it does
// nothing when:
//   - The binary is a "dev" build (Version == "dev"). Local devs use
//     `make install` and shouldn't fight an auto-installer.
//   - The skill isn't installed for that harness (SKILL.md missing).
//     Treat this as an explicit per-harness opt-out; never re-install.
//   - That harness's sidecar already matches Version. The common path.
//
// All errors are silent — auto-upgrade is best-effort plumbing, not a
// command. A user-visible failure here would be far more annoying
// than the eventual symptom of a stale skill.
func maybeAutoUpgradeSkill() {
	if Version == "" || Version == "dev" {
		return
	}
	for _, h := range allHarnesses() {
		if !skillInstalled(h) {
			continue // opted out; don't reinstall behind their back
		}
		if readSkillVersion(h) == Version {
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
	only := harnessFlag(fs)
	if err := fs.Parse(args); err != nil {
		return flagParseRC(err)
	}
	targets, ok := resolveTargets(*only, presentHarnesses)
	if !ok {
		return 2
	}
	rc := 0
	for _, h := range targets {
		if !installSkillFor(h, *force, *skipHook) {
			rc = 1
		}
	}
	return rc
}

func installSkillFor(h harness.Harness, force, skipHook bool) bool {
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
	installHooks(h, false)
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
		return flagParseRC(err)
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
	if _, err := os.Stat(skillDir); os.IsNotExist(err) {
		fmt.Printf("%s: flow skill not installed at %s — nothing to do\n", name, skillDir)
	} else {
		if err := h.UninstallSkill(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", name, err)
			return false
		}
		fmt.Printf("%s: uninstalled flow skill from %s\n", name, skillDir)
	}
	if keepHook {
		fmt.Printf("%s: --keep-hook: leaving hooks in place\n", name)
		return true
	}
	hooks := append(flowHooks(), flowHook{"PostToolUse", postToolUseHookCommand, "", nil,
		harness.Harness.UninstallPostToolUseHook})
	for _, fh := range hooks {
		if removed, err := fh.remove(h, fh.command); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: could not remove %s hook: %v\n", name, fh.event, err)
		} else if removed {
			fmt.Printf("%s: removed %s hook\n", name, fh.event)
		}
	}
	return true
}
