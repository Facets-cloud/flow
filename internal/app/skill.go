package app

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed skill/SKILL.md
var embeddedSkill []byte

// hookCommand is the exact string written into settings.json under
// hooks.SessionStart so install/uninstall can idempotently find it.
// Keep it stable — changing this string would orphan existing
// installations.
const hookCommand = "flow hook session-start"

// hookMatcher is the SessionStart matcher string — fires on both
// fresh startup and `claude --resume`.
const hookMatcher = "startup|resume"

// userPromptSubmitHookCommand is the exact string written into
// settings.json under hooks.UserPromptSubmit. Same stability rule as
// hookCommand: changing this string would orphan existing installs.
const userPromptSubmitHookCommand = "flow hook user-prompt-submit"

// skillTarget describes one agent installation destination for the
// flow skill.
//
// presenceSubdir and installSubdir intentionally diverge: most agents
// install into their own `~/.<agent>/skills/`, but some (Gemini)
// scan the shared `~/.agents/skills/` root in addition to their own
// dir, so installing to both would double-load the skill.
type skillTarget struct {
	agent          string // "claude", "codex", "cursor", "gemini"
	presenceSubdir string // dir under $HOME that signals "agent is installed"
	installSubdir  string // dir under $HOME where SKILL.md goes (parent of skills/flow)
}

// skillTargetCandidates enumerates the agents flow knows how to
// install to. Claude is always installed (canonical legacy target —
// `flow init` has always created `~/.claude/skills/flow/` even if the
// user hadn't started Claude yet). Other agents are auto-discovered
// by checking presenceSubdir on disk.
//
// Why presence and install dirs differ for Gemini: Gemini CLI scans
// `~/.agents/skills/` as a shared multi-agent skills root in addition
// to its per-agent `~/.gemini/skills/`. Writing to both produces a
// duplicate-load. The shared root is the better target because other
// tools (plugins, marketplaces) also discover skills there.
var skillTargetCandidates = []skillTarget{
	{agent: "claude", presenceSubdir: ".claude", installSubdir: ".claude"},
	{agent: "codex", presenceSubdir: ".codex", installSubdir: ".codex"},
	{agent: "cursor", presenceSubdir: ".cursor", installSubdir: ".cursor"},
	{agent: "gemini", presenceSubdir: ".gemini", installSubdir: ".agents"},
}

// skillPathFor returns the absolute SKILL.md path for a candidate
// under the given home dir.
func (t skillTarget) skillPathUnder(home string) string {
	return filepath.Join(home, t.installSubdir, "skills", "flow", "SKILL.md")
}

// presenceDirUnder returns the absolute path checked to decide
// whether the agent is installed.
func (t skillTarget) presenceDirUnder(home string) string {
	return filepath.Join(home, t.presenceSubdir)
}

// resolvedSkillTarget is a skillTarget after $HOME has been resolved
// — it carries absolute paths so callers don't need to recompute.
type resolvedSkillTarget struct {
	agent     string
	skillPath string // absolute path to SKILL.md
}

// skillTargets returns every install target that should receive
// SKILL.md right now. Claude is always present. Other agents are
// included only when their presence dir exists, so we never pollute
// `$HOME` for agents the user doesn't have.
func skillTargets() ([]resolvedSkillTarget, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("no home dir: %w", err)
	}
	var out []resolvedSkillTarget
	for _, c := range skillTargetCandidates {
		if c.agent != "claude" {
			if _, err := os.Stat(c.presenceDirUnder(home)); err != nil {
				continue
			}
		}
		out = append(out, resolvedSkillTarget{
			agent:     c.agent,
			skillPath: c.skillPathUnder(home),
		})
	}
	return out, nil
}

// claudeSkillInstallPath returns the canonical Claude skill path.
// Kept as a thin helper because the SessionStart hook is Claude-only
// and a few callers care specifically about the Claude path.
func claudeSkillInstallPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "skills", "flow", "SKILL.md"), nil
}

// versionPathFor returns the VERSION sidecar path adjacent to the
// given SKILL.md path: `<dir-of-skill>/VERSION`.
func versionPathFor(skillPath string) string {
	return filepath.Join(filepath.Dir(skillPath), "VERSION")
}

// readVersionAt returns the recorded version string at the given
// VERSION sidecar path, or "" if the file is missing or unreadable.
func readVersionAt(versionPath string) string {
	b, err := os.ReadFile(versionPath)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// writeVersionAt records v as the installed version at the given
// VERSION sidecar path. Errors are non-fatal at the call sites —
// failing to write the sidecar should never block a successful skill
// install.
func writeVersionAt(versionPath, v string) error {
	if err := os.MkdirAll(filepath.Dir(versionPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(versionPath, []byte(v+"\n"), 0o644)
}

// userSettingsPath returns ~/.claude/settings.json.
func userSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// maybeAutoUpgradeSkill checks the recorded skill version against the
// running binary's version and, if they differ, refreshes the skill +
// SessionStart hook. Designed to run on every flow invocation so the
// user gets a self-healing upgrade flow after replacing the binary.
//
// The check is intentionally conservative — it does nothing when:
//   - The binary is a "dev" build (Version == "dev"). Local devs use
//     `make install` and shouldn't fight an auto-installer.
//   - The skill isn't installed at all (sentinel: SKILL.md missing).
//     Treat this as an explicit user opt-out; never re-install.
//   - The recorded version already matches Version. The common path.
//
// All errors are silent — auto-upgrade is best-effort plumbing, not a
// command. A user-visible failure here would be far more annoying than
// the eventual symptom of a stale skill.
func maybeAutoUpgradeSkill() {
	if Version == "" || Version == "dev" {
		return
	}
	targets, err := skillTargets()
	if err != nil {
		return
	}
	upgraded := false
	for _, t := range targets {
		if _, err := os.Stat(t.skillPath); err != nil {
			// Not installed for this agent → user opted out; don't
			// reinstall behind their back.
			continue
		}
		if readVersionAt(versionPathFor(t.skillPath)) == Version {
			continue
		}
		if err := os.WriteFile(t.skillPath, embeddedSkill, 0o644); err != nil {
			continue
		}
		_ = writeVersionAt(versionPathFor(t.skillPath), Version)
		upgraded = true
	}
	// SessionStart hook + stale UserPromptSubmit cleanup are Claude-only.
	if upgraded {
		_, _ = installSessionStartHook()
		_, _ = uninstallUserPromptSubmitHook()
		fmt.Fprintf(os.Stderr, "flow: upgraded skill to %s\n", Version)
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

func skillInstall(args []string, forceDefault bool) int {
	fs := flagSet("skill install")
	force := fs.Bool("force", forceDefault, "overwrite an existing installation")
	skipHook := fs.Bool("skip-hook", false, "don't auto-install the SessionStart hook in ~/.claude/settings.json")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	targets, err := skillTargets()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Pre-check: if any target already has SKILL.md and the user did
	// not pass --force, refuse the whole batch. Partial overwrites are
	// confusing; one of two states is much easier to reason about.
	if !*force {
		for _, t := range targets {
			if _, err := os.Stat(t.skillPath); err == nil {
				fmt.Fprintf(os.Stderr, "error: %s already exists; use --force to overwrite\n", t.skillPath)
				return 1
			} else if !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "error: stat %s: %v\n", t.skillPath, err)
				return 1
			}
		}
	}

	for _, t := range targets {
		if err := os.MkdirAll(filepath.Dir(t.skillPath), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "error: create %s: %v\n", filepath.Dir(t.skillPath), err)
			return 1
		}
		if err := os.WriteFile(t.skillPath, embeddedSkill, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "error: write %s: %v\n", t.skillPath, err)
			return 1
		}
		if err := writeVersionAt(versionPathFor(t.skillPath), Version); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not record skill version for %s: %v\n", t.agent, err)
		}
		fmt.Printf("installed flow skill to %s\n", t.skillPath)
	}

	if *skipHook {
		fmt.Println("--skip-hook: leaving ~/.claude/settings.json alone")
		return 0
	}
	if added, err := installSessionStartHook(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not install SessionStart hook: %v\n", err)
		// Non-fatal: the skill is still usable without the hook; the
		// user can wire it manually. Return 0 so `flow init` doesn't
		// fail on a settings.json quirk.
		return 0
	} else if added {
		settings, _ := userSettingsPath()
		fmt.Printf("installed SessionStart hook in %s (fires on startup + resume)\n", settings)
	} else {
		fmt.Println("SessionStart hook already installed — leaving as is")
	}
	// UserPromptSubmit hook was removed in v0.1.0-alpha.7. Actively
	// uninstall any stale entry left behind by older binaries so a
	// fresh `flow skill install` (or `update`) leaves a clean
	// settings.json.
	if removed, err := uninstallUserPromptSubmitHook(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove stale UserPromptSubmit hook: %v\n", err)
		return 0
	} else if removed {
		settings, _ := userSettingsPath()
		fmt.Printf("removed stale UserPromptSubmit hook from %s (no longer used)\n", settings)
	}
	return 0
}

func skillUninstall(args []string) int {
	fs := flagSet("skill uninstall")
	keepHook := fs.Bool("keep-hook", false, "don't remove the SessionStart hook from ~/.claude/settings.json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// Uninstall iterates the full candidate set, NOT just live targets,
	// so we still clean up agents whose home dir disappeared between
	// install and uninstall. We discover by checking the actual skill
	// dir path on disk for every known agent.
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: no home dir: %v\n", err)
		return 1
	}
	anyRemoved := false
	for _, c := range skillTargetCandidates {
		skillDir := filepath.Dir(c.skillPathUnder(home))
		if _, err := os.Stat(skillDir); os.IsNotExist(err) {
			continue
		} else if err != nil {
			fmt.Fprintf(os.Stderr, "warning: stat %s: %v\n", skillDir, err)
			continue
		}
		if err := os.RemoveAll(skillDir); err != nil {
			fmt.Fprintf(os.Stderr, "error: remove %s: %v\n", skillDir, err)
			return 1
		}
		fmt.Printf("uninstalled flow skill from %s\n", skillDir)
		anyRemoved = true
	}
	if !anyRemoved {
		fmt.Println("flow skill not installed anywhere — nothing to do")
	}

	if *keepHook {
		fmt.Println("--keep-hook: leaving SessionStart hook in settings.json")
		return 0
	}
	if removed, err := uninstallSessionStartHook(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove SessionStart hook: %v\n", err)
		return 0
	} else if removed {
		settings, _ := userSettingsPath()
		fmt.Printf("removed SessionStart hook from %s\n", settings)
	}
	if removed, err := uninstallUserPromptSubmitHook(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove UserPromptSubmit hook: %v\n", err)
		return 0
	} else if removed {
		settings, _ := userSettingsPath()
		fmt.Printf("removed UserPromptSubmit hook from %s\n", settings)
	}
	return 0
}

// installSessionStartHook idempotently adds the flow SessionStart hook
// to ~/.claude/settings.json. Thin wrapper around installClaudeHook.
func installSessionStartHook() (bool, error) {
	return installClaudeHook("SessionStart", hookMatcher, hookCommand)
}

// uninstallSessionStartHook removes the flow SessionStart hook entry.
// Thin wrapper around uninstallClaudeHook.
func uninstallSessionStartHook() (bool, error) {
	return uninstallClaudeHook("SessionStart", hookCommand)
}

// uninstallUserPromptSubmitHook removes any stale flow UserPromptSubmit
// hook entry from ~/.claude/settings.json. The hook itself was
// removed in v0.1.0-alpha.7 — see cmdHookUserPromptSubmit. Both
// `flow skill install` and `maybeAutoUpgradeSkill` call this on every
// upgrade so existing-user installs converge to a clean settings.json.
func uninstallUserPromptSubmitHook() (bool, error) {
	return uninstallClaudeHook("UserPromptSubmit", userPromptSubmitHookCommand)
}

// installClaudeHook idempotently adds a hook entry for the given
// Claude Code event to ~/.claude/settings.json. matcher may be empty —
// some events (UserPromptSubmit, Notification) don't use a matcher and
// the field is omitted from the entry. command is both the literal
// command Claude Code will execute AND the marker we look for to decide
// whether the hook is already installed.
//
// Returns (added, err) where added is true iff the file was modified.
// The merge preserves all existing top-level keys, all hooks under
// other events, and all existing entries under the same event.
func installClaudeHook(event, matcher, command string) (bool, error) {
	path, err := userSettingsPath()
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("read %s: %w", path, err)
		}
		raw = []byte("{}")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
		}
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	if settings == nil {
		settings = map[string]any{}
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	entries, _ := hooks[event].([]any)

	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		inner, _ := m["hooks"].([]any)
		for _, h := range inner {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmd, _ := hm["command"].(string); cmd == command {
				return false, nil
			}
		}
	}

	newEntry := map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": command,
			},
		},
	}
	if matcher != "" {
		newEntry["matcher"] = matcher
	}
	entries = append(entries, newEntry)
	hooks[event] = entries
	settings["hooks"] = hooks

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal settings: %w", err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// uninstallClaudeHook removes any entry under hooks.<event> whose
// inner hook list contains a command matching the given marker.
// Returns (removed, err) where removed is true iff the file changed.
func uninstallClaudeHook(event, command string) (bool, error) {
	path, err := userSettingsPath()
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return false, nil
	}
	entries, _ := hooks[event].([]any)
	if len(entries) == 0 {
		return false, nil
	}

	changed := false
	kept := make([]any, 0, len(entries))
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			kept = append(kept, entry)
			continue
		}
		inner, _ := m["hooks"].([]any)
		filteredInner := make([]any, 0, len(inner))
		for _, h := range inner {
			hm, ok := h.(map[string]any)
			if !ok {
				filteredInner = append(filteredInner, h)
				continue
			}
			cmd, _ := hm["command"].(string)
			if strings.TrimSpace(cmd) == command {
				changed = true
				continue
			}
			filteredInner = append(filteredInner, h)
		}
		if len(filteredInner) == 0 {
			changed = true
			continue
		}
		m["hooks"] = filteredInner
		kept = append(kept, m)
	}

	if !changed {
		return false, nil
	}
	if len(kept) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = kept
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	} else {
		settings["hooks"] = hooks
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal settings: %w", err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}
