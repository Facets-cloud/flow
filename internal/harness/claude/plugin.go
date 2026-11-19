package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"flow/internal/harness"

	"golang.org/x/mod/semver"
)

// flow's hooks reach Claude Code through a skills-dir plugin: any
// ~/.claude/skills/<name>/ holding .claude-plugin/plugin.json loads in place
// as <name>@skills-dir, enabled by default, with no marketplace and no
// settings.json write. flow already owns ~/.claude/skills/flow/, so the
// plugin is two more files there:
//
//	.claude-plugin/plugin.json  manifest — deliberately NO "skills" key: the
//	                            root SKILL.md keeps loading as the plain
//	                            `flow` skill (a "skills" key or a
//	                            skills/flow/ subdir would namespace it as
//	                            flow:flow)
//	hooks/hooks.json            same schema as settings.json hooks
//
// Plugin hooks merge with settings hooks and a handler present in both
// fires twice, which is why the plugin commands carry `--src plugin` and
// the legacy (settings) handlers stand down for a session the plugin
// already covers (see app/hookguard.go).

// PluginID is the id Claude Code gives the flow skills-dir plugin; the key
// a user's `claude plugin disable` writes under enabledPlugins.
const PluginID = "flow@skills-dir"

func (c *claude) pluginDir() (string, error) {
	p, err := c.SkillInstallPath()
	if err != nil {
		return "", err
	}
	return filepath.Dir(p), nil
}

// PluginManifestPath returns ~/.claude/skills/flow/.claude-plugin/plugin.json.
func (c *claude) PluginManifestPath() (string, error) {
	d, err := c.pluginDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, ".claude-plugin", "plugin.json"), nil
}

// PluginHooksPath returns ~/.claude/skills/flow/hooks/hooks.json.
func (c *claude) PluginHooksPath() (string, error) {
	d, err := c.pluginDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "hooks", "hooks.json"), nil
}

// pluginManifest renders plugin.json. version is included only when it is
// a semver (a dev build has none worth recording).
func pluginManifest(version string) ([]byte, error) {
	m := map[string]any{
		"name":        "flow",
		"description": "flow task + session manager hooks (session reload, bus nudges, wake)",
	}
	if v := strings.TrimSpace(version); semver.IsValid(v) {
		m["version"] = strings.TrimPrefix(v, "v")
	}
	return marshalJSON(m)
}

// matcherFor is the matcher a hook event uses in Claude Code, identical to
// the one the legacy settings.json entries carried.
func matcherFor(event string) string {
	switch event {
	case "SessionStart":
		return hookMatcher
	case "PreToolUse", "PostToolUse":
		return toolMatcher
	}
	return ""
}

// pluginHooksJSON renders hooks/hooks.json for hooks, one entry per hook
// in the given order. A rewake hook gets the asyncRewake fields the
// settings.json form had (installAsyncHook).
func pluginHooksJSON(hooks []harness.PluginHook) ([]byte, error) {
	byEvent := map[string][]any{}
	for _, h := range hooks {
		cmd := map[string]any{"type": "command", "command": h.Command}
		if h.Rewake {
			cmd["asyncRewake"] = true
			cmd["timeout"] = rewakeTimeout
			cmd["rewakeMessage"] = h.RewakeMessage
		}
		entry := map[string]any{"hooks": []any{cmd}}
		if m := matcherFor(h.Event); m != "" {
			entry["matcher"] = m
		}
		byEvent[h.Event] = append(byEvent[h.Event], entry)
	}
	return marshalJSON(map[string]any{"hooks": byEvent})
}

func marshalJSON(v any) ([]byte, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// writeIfChanged writes content to path (creating parents) unless the file
// already holds exactly that content.
func writeIfChanged(path string, content []byte) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, content) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

func (c *claude) InstallPluginHooks(version string, hooks []harness.PluginHook) (bool, error) {
	mp, err := c.PluginManifestPath()
	if err != nil {
		return false, err
	}
	hp, err := c.PluginHooksPath()
	if err != nil {
		return false, err
	}
	manifest, err := pluginManifest(version)
	if err != nil {
		return false, err
	}
	hj, err := pluginHooksJSON(hooks)
	if err != nil {
		return false, err
	}
	// Hooks first: a manifest without its hook file would load an empty
	// plugin.
	a, err := writeIfChanged(hp, hj)
	if err != nil {
		return a, err
	}
	b, err := writeIfChanged(mp, manifest)
	return a || b, err
}

func (c *claude) UninstallPluginHooks() (bool, error) {
	removed := false
	for _, get := range []func() (string, error){c.PluginManifestPath, c.PluginHooksPath} {
		p, err := get()
		if err != nil {
			return removed, err
		}
		if err := os.Remove(p); err == nil {
			removed = true
		} else if !os.IsNotExist(err) {
			return removed, err
		}
		_ = os.Remove(filepath.Dir(p)) // only succeeds when empty
	}
	return removed, nil
}

func (c *claude) PluginInstalled() bool {
	for _, get := range []func() (string, error){c.PluginManifestPath, c.PluginHooksPath} {
		p, err := get()
		if err != nil {
			return false
		}
		if _, err := os.Stat(p); err != nil {
			return false
		}
	}
	return true
}

// PluginDisabled reports enabledPlugins["flow@skills-dir"] == false in
// ~/.claude/settings.json — what `claude plugin disable flow@skills-dir`
// writes. Read-only: flow never re-enables a plugin the user turned off.
func (c *claude) PluginDisabled() bool {
	path, err := settingsPath()
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var s struct {
		EnabledPlugins map[string]any `json:"enabledPlugins"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return false
	}
	v, ok := s.EnabledPlugins[PluginID]
	return ok && v == false
}

// RemoveSkillKeepPlugin removes everything in the skill dir except the
// plugin manifest dir and the hooks dir.
func (c *claude) RemoveSkillKeepPlugin() error {
	dir, err := c.pluginDir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == ".claude-plugin" || e.Name() == "hooks" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
