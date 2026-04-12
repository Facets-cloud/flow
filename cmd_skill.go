package main

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
const hookCommand = "~/.flow/bin/flow hook session-start"

// hookMatcher is the SessionStart matcher string — fires on both
// fresh startup and `claude --resume`.
const hookMatcher = "startup|resume"

// skillInstallPath returns the absolute path where the skill should be
// installed on disk: ~/.claude/skills/flow/SKILL.md.
func skillInstallPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "skills", "flow", "SKILL.md"), nil
}

// userSettingsPath returns ~/.claude/settings.json.
func userSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
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

	dest, err := skillInstallPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if _, err := os.Stat(dest); err == nil && !*force {
		fmt.Fprintf(os.Stderr, "error: %s already exists; use --force to overwrite\n", dest)
		return 1
	} else if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error: stat %s: %v\n", dest, err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: create %s: %v\n", filepath.Dir(dest), err)
		return 1
	}
	if err := os.WriteFile(dest, embeddedSkill, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: write %s: %v\n", dest, err)
		return 1
	}
	fmt.Printf("installed flow skill to %s\n", dest)

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
	return 0
}

func skillUninstall(args []string) int {
	fs := flagSet("skill uninstall")
	keepHook := fs.Bool("keep-hook", false, "don't remove the SessionStart hook from ~/.claude/settings.json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dest, err := skillInstallPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
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
	return 0
}

// installSessionStartHook idempotently adds the flow SessionStart hook
// to ~/.claude/settings.json. Returns (added, err) where added is true
// if the settings file was actually modified (false if the hook was
// already present).
//
// The merge preserves all existing top-level keys, all existing hooks
// under other events (PreToolUse, etc.), and all existing SessionStart
// entries. It only appends a new entry if no existing SessionStart
// entry references `~/.flow/bin/flow hook session-start`.
func installSessionStartHook() (bool, error) {
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
	sessionStart, _ := hooks["SessionStart"].([]any)

	// Walk existing SessionStart entries; if any inner hook's command
	// equals our marker, it's already installed.
	for _, entry := range sessionStart {
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
			if cmd, _ := hm["command"].(string); cmd == hookCommand {
				return false, nil
			}
		}
	}

	// Not present — append our entry.
	newEntry := map[string]any{
		"matcher": hookMatcher,
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": hookCommand,
			},
		},
	}
	sessionStart = append(sessionStart, newEntry)
	hooks["SessionStart"] = sessionStart
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

// uninstallSessionStartHook removes any SessionStart entry whose inner
// hooks reference the flow hook command. Returns (removed, err) where
// removed is true if the settings file was actually modified.
func uninstallSessionStartHook() (bool, error) {
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
	sessionStart, _ := hooks["SessionStart"].([]any)
	if len(sessionStart) == 0 {
		return false, nil
	}

	changed := false
	kept := make([]any, 0, len(sessionStart))
	for _, entry := range sessionStart {
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
			if strings.TrimSpace(cmd) == hookCommand {
				changed = true
				continue
			}
			filteredInner = append(filteredInner, h)
		}
		if len(filteredInner) == 0 {
			// Entry had only our hook → drop the entry entirely.
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
		delete(hooks, "SessionStart")
	} else {
		hooks["SessionStart"] = kept
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
