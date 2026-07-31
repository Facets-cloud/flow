// Package hooksettings edits a Claude-compatible settings.json hook
// registry in place. It is a leaf package with zero non-stdlib imports,
// shared by every harness adapter whose hook config uses the
//
//	{"hooks": {"<Event>": [{"matcher": "…", "hooks": [{"type": "command",
//	                                                  "command": "…"}]}]}}
//
// shape — currently Claude Code (~/.claude/settings.json) and Praxis
// (~/.praxis/agent/settings.json, whose hook loader is Claude-compatible:
// it compiles `matcher` as a regexp and executes `command` entries the
// same way). Adapters differ only in which file they point at, so the
// path is a parameter and the mutation logic lives here once.
//
// Both operations preserve everything they don't own: unrelated
// top-level settings keys, other events, and sibling entries under the
// same event. Entries are matched by their inner `command` string, which
// doubles as the installation marker — so the command strings flow
// records are load-bearing and must stay stable across releases.
package hooksettings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Install idempotently adds a hook entry for `event` to the settings
// file at `path`, creating the file (and its parent directory) if
// absent. matcher may be empty — some events don't use one and the
// field is then omitted rather than written as "".
//
// Returns (added=true) iff the file was actually modified; an entry
// whose inner command already equals `command` is left alone.
func Install(path, event, matcher, command string) (bool, error) {
	settings, err := load(path)
	if err != nil {
		return false, err
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	entries, _ := hooks[event].([]any)
	if findCommand(entries, command) {
		return false, nil
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
	hooks[event] = append(entries, newEntry)
	settings["hooks"] = hooks

	return true, save(path, settings)
}

// Uninstall removes every entry under `event` whose inner command
// matches `command`, pruning entries (and then the event, and then the
// hooks map) that end up empty. A missing settings file is not an
// error — there is nothing to remove.
//
// Returns (removed=true) iff the file was actually modified.
func Uninstall(path, event, command string) (bool, error) {
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

	return true, save(path, settings)
}

// load reads and decodes the settings file, treating a missing file as
// an empty object (and creating its parent directory so the subsequent
// save can succeed).
func load(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		raw = []byte("{}")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
		}
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if settings == nil {
		// Literal `null` decodes to a nil map.
		settings = map[string]any{}
	}
	return settings, nil
}

// save writes settings back as indented JSON with a trailing newline.
func save(path string, settings map[string]any) error {
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// findCommand reports whether any entry carries an inner hook whose
// command equals `command`.
func findCommand(entries []any, command string) bool {
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
				return true
			}
		}
	}
	return false
}
