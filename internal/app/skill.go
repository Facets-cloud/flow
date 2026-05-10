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

// skillInstallPath returns the Claude skill path for backward-compatible
// callers. skillInstallPaths is used for install/update so both Claude
// and Codex receive the same operating manual.
func skillInstallPath() (string, error) {
	return claudeSkillInstallPath()
}

func claudeSkillInstallPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "skills", "flow", "SKILL.md"), nil
}

func codexSkillInstallPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".codex", "skills", "flow", "SKILL.md"), nil
}

func skillInstallPaths() ([]string, error) {
	claude, err := claudeSkillInstallPath()
	if err != nil {
		return nil, err
	}
	codex, err := codexSkillInstallPath()
	if err != nil {
		return nil, err
	}
	return []string{claude, codex}, nil
}

// skillVersionPath returns the sidecar file that records which binary
// version installed the current SKILL.md — used by the auto-upgrade
// check to decide whether to refresh the skill.
func skillVersionPath() (string, error) {
	skill, err := skillInstallPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(skill), "VERSION"), nil
}

// readSkillVersion returns the recorded version string, or "" if the
// sidecar file is missing or unreadable.
func readSkillVersion() string {
	p, err := skillVersionPath()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// writeSkillVersion records v as the version of the binary that
// installed the current SKILL.md. Errors are non-fatal — failing to
// write the sidecar should never block a successful skill install.
func writeSkillVersion(v string) error {
	p, err := skillVersionPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(v+"\n"), 0o644)
}

// userSettingsPath returns ~/.claude/settings.json.
func userSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

func codexHooksPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".codex", "hooks.json"), nil
}

func codexConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
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
	paths, err := skillInstallPaths()
	if err != nil {
		return
	}
	installed := false
	for _, skillPath := range paths {
		if _, err := os.Stat(skillPath); err == nil {
			installed = true
			break
		}
	}
	if !installed {
		// Not installed → user opted out; don't reinstall behind their back.
		return
	}
	if readSkillVersion() == Version {
		return
	}
	// Version mismatch — refresh skill bytes and the SessionStart hook.
	for _, skillPath := range paths {
		if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
			return
		}
		if err := os.WriteFile(skillPath, embeddedSkill, 0o644); err != nil {
			return
		}
	}
	_ = writeSkillVersion(Version)
	_, _ = installSessionStartHook()
	_, _ = ensureCodexHooksFeature()
	_, _ = installCodexSessionStartHook()
	// UserPromptSubmit hook was removed in v0.1.0-alpha.7 — the
	// per-prompt token cost wasn't worth the marginal value. Actively
	// uninstall any stale entry left behind by older binaries.
	_, _ = uninstallUserPromptSubmitHook()
	_, _ = uninstallCodexUserPromptSubmitHook()
	fmt.Fprintf(os.Stderr, "flow: upgraded skill to %s\n", Version)
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
	skipHook := fs.Bool("skip-hook", false, "don't auto-install SessionStart hooks in ~/.claude/settings.json and ~/.codex/hooks.json")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	paths, err := skillInstallPaths()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	for _, dest := range paths {
		if _, err := os.Stat(dest); err == nil && !*force {
			fmt.Fprintf(os.Stderr, "error: %s already exists; use --force to overwrite\n", dest)
			return 1
		} else if err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "error: stat %s: %v\n", dest, err)
			return 1
		}
	}
	for _, dest := range paths {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "error: create %s: %v\n", filepath.Dir(dest), err)
			return 1
		}
		if err := os.WriteFile(dest, embeddedSkill, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "error: write %s: %v\n", dest, err)
			return 1
		}
		fmt.Printf("installed flow skill to %s\n", dest)
	}
	if err := writeSkillVersion(Version); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not record skill version: %v\n", err)
	}

	if *skipHook {
		fmt.Println("--skip-hook: leaving Claude/Codex hook config alone")
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
	if added, err := ensureCodexHooksFeature(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not enable Codex hooks feature: %v\n", err)
	} else if added {
		config, _ := codexConfigPath()
		fmt.Printf("enabled Codex hooks in %s\n", config)
	}
	if added, err := installCodexSessionStartHook(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not install Codex SessionStart hook: %v\n", err)
	} else if added {
		hooks, _ := codexHooksPath()
		fmt.Printf("installed Codex SessionStart hook in %s (fires on startup + resume)\n", hooks)
	} else {
		fmt.Println("Codex SessionStart hook already installed — leaving as is")
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
	if removed, err := uninstallCodexUserPromptSubmitHook(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove stale Codex UserPromptSubmit hook: %v\n", err)
		return 0
	} else if removed {
		hooks, _ := codexHooksPath()
		fmt.Printf("removed stale UserPromptSubmit hook from %s (no longer used)\n", hooks)
	}
	return 0
}

func skillUninstall(args []string) int {
	fs := flagSet("skill uninstall")
	keepHook := fs.Bool("keep-hook", false, "don't remove SessionStart hooks from Claude/Codex config")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	paths, err := skillInstallPaths()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	for _, dest := range paths {
		skillDir := filepath.Dir(dest)
		if _, err := os.Stat(skillDir); os.IsNotExist(err) {
			fmt.Printf("flow skill not installed at %s — nothing to do\n", skillDir)
		} else {
			if err := os.RemoveAll(skillDir); err != nil {
				fmt.Fprintf(os.Stderr, "error: remove %s: %v\n", skillDir, err)
				return 1
			}
			fmt.Printf("uninstalled flow skill from %s\n", skillDir)
		}
	}

	if *keepHook {
		fmt.Println("--keep-hook: leaving SessionStart hooks in config")
		return 0
	}
	if removed, err := uninstallSessionStartHook(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove SessionStart hook: %v\n", err)
		return 0
	} else if removed {
		settings, _ := userSettingsPath()
		fmt.Printf("removed SessionStart hook from %s\n", settings)
	}
	if removed, err := uninstallCodexSessionStartHook(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove Codex SessionStart hook: %v\n", err)
		return 0
	} else if removed {
		hooks, _ := codexHooksPath()
		fmt.Printf("removed Codex SessionStart hook from %s\n", hooks)
	}
	if removed, err := uninstallUserPromptSubmitHook(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove UserPromptSubmit hook: %v\n", err)
		return 0
	} else if removed {
		settings, _ := userSettingsPath()
		fmt.Printf("removed UserPromptSubmit hook from %s\n", settings)
	}
	if removed, err := uninstallCodexUserPromptSubmitHook(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove Codex UserPromptSubmit hook: %v\n", err)
		return 0
	} else if removed {
		hooks, _ := codexHooksPath()
		fmt.Printf("removed Codex UserPromptSubmit hook from %s\n", hooks)
	}
	return 0
}

// installSessionStartHook idempotently adds the flow SessionStart hook
// to ~/.claude/settings.json. Thin wrapper around installClaudeHook.
func installSessionStartHook() (bool, error) {
	return installClaudeHook("SessionStart", hookMatcher, hookCommand)
}

func installCodexSessionStartHook() (bool, error) {
	return installCodexHook("SessionStart", hookMatcher, hookCommand)
}

// uninstallSessionStartHook removes the flow SessionStart hook entry.
// Thin wrapper around uninstallClaudeHook.
func uninstallSessionStartHook() (bool, error) {
	return uninstallClaudeHook("SessionStart", hookCommand)
}

func uninstallCodexSessionStartHook() (bool, error) {
	return uninstallCodexHook("SessionStart", hookCommand)
}

// uninstallUserPromptSubmitHook removes any stale flow UserPromptSubmit
// hook entry from ~/.claude/settings.json. The hook itself was
// removed in v0.1.0-alpha.7 — see cmdHookUserPromptSubmit. Both
// `flow skill install` and `maybeAutoUpgradeSkill` call this on every
// upgrade so existing-user installs converge to a clean settings.json.
func uninstallUserPromptSubmitHook() (bool, error) {
	return uninstallClaudeHook("UserPromptSubmit", userPromptSubmitHookCommand)
}

func uninstallCodexUserPromptSubmitHook() (bool, error) {
	return uninstallCodexHook("UserPromptSubmit", userPromptSubmitHookCommand)
}

func ensureCodexHooksFeature() (bool, error) {
	path, err := codexConfigPath()
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("read %s: %w", path, err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
		}
		raw = nil
	}
	text := string(raw)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "codex_hooks") {
			if strings.Contains(trim, "true") {
				return false, nil
			}
			lines[i] = "codex_hooks = true"
			return writeCodexConfig(path, lines)
		}
	}
	for i, line := range lines {
		if strings.TrimSpace(line) == "[features]" {
			lines = append(lines[:i+1], append([]string{"codex_hooks = true"}, lines[i+1:]...)...)
			return writeCodexConfig(path, lines)
		}
	}
	if strings.TrimSpace(text) != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += "\n[features]\ncodex_hooks = true\n"
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

func writeCodexConfig(path string, lines []string) (bool, error) {
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

func installCodexHook(event, matcher, command string) (bool, error) {
	path, err := codexHooksPath()
	if err != nil {
		return false, err
	}
	return installHookInPath(path, event, matcher, command)
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
	return installHookInPath(path, event, matcher, command)
}

func installHookInPath(path, event, matcher, command string) (bool, error) {
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

func uninstallCodexHook(event, command string) (bool, error) {
	path, err := codexHooksPath()
	if err != nil {
		return false, err
	}
	return uninstallHookInPath(path, event, command)
}

// uninstallClaudeHook removes any entry under hooks.<event> whose
// inner hook list contains a command matching the given marker.
// Returns (removed, err) where removed is true iff the file changed.
func uninstallClaudeHook(event, command string) (bool, error) {
	path, err := userSettingsPath()
	if err != nil {
		return false, err
	}
	return uninstallHookInPath(path, event, command)
}

func uninstallHookInPath(path, event, command string) (bool, error) {
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
