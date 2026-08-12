package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func hooksPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".codex", "hooks.json"), nil
}

func installHook(event, matcher, command string) (bool, error) {
	path, err := hooksPath()
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
		m, _ := entry.(map[string]any)
		inner, _ := m["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if cmd, _ := hm["command"].(string); cmd == command {
				return false, nil
			}
		}
	}
	entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}
	if matcher != "" {
		entry["matcher"] = matcher
	}
	hooks[event] = append(entries, entry)
	settings["hooks"] = hooks
	return writeSettings(path, settings)
}

func uninstallHook(event, command string) (bool, error) {
	path, err := hooksPath()
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	hooks, _ := settings["hooks"].(map[string]any)
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
		filtered := make([]any, 0, len(inner))
		for _, h := range inner {
			hm, ok := h.(map[string]any)
			if ok && strings.TrimSpace(stringValue(hm["command"])) == command {
				changed = true
				continue
			}
			filtered = append(filtered, h)
		}
		if len(filtered) == 0 {
			changed = true
			continue
		}
		m["hooks"] = filtered
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
	return writeSettings(path, settings)
}

func stringValue(v any) string { s, _ := v.(string); return s }

func writeSettings(path string, settings map[string]any) (bool, error) {
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal %s: %w", path, err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}
