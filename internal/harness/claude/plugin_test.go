package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"flow/internal/harness"
)

func TestPluginInstallUninstallRoundtrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	c := &claude{}
	hooks := []harness.PluginHook{
		{Event: "SessionStart", Command: "flow hook session-start --src plugin"},
		{Event: "PreToolUse", Command: "flow hook pre-tool-use --src plugin"},
		{Event: "Stop", Command: "flow inbox wait --rewake --src plugin", Rewake: true, RewakeMessage: "flow bus:"},
	}
	if c.PluginInstalled() {
		t.Fatal("not installed yet")
	}
	changed, err := c.InstallPluginHooks("v0.2.0", hooks)
	if err != nil || !changed {
		t.Fatalf("install changed=%v err=%v", changed, err)
	}
	if changed, _ := c.InstallPluginHooks("v0.2.0", hooks); changed {
		t.Error("identical reinstall must report no change")
	}
	if !c.PluginInstalled() {
		t.Fatal("should be installed")
	}
	hp, _ := c.PluginHooksPath()
	raw, _ := os.ReadFile(hp)
	var got struct {
		Hooks map[string][]struct {
			Matcher string           `json:"matcher"`
			Hooks   []map[string]any `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Hooks["SessionStart"][0].Matcher != hookMatcher || got.Hooks["PreToolUse"][0].Matcher != toolMatcher {
		t.Errorf("matchers wrong: %s", raw)
	}
	if st := got.Hooks["Stop"][0]; st.Matcher != "" || st.Hooks[0]["asyncRewake"] != true {
		t.Errorf("stop rewake entry wrong: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Error("plugin install must not create settings.json")
	}
	removed, err := c.UninstallPluginHooks()
	if err != nil || !removed || c.PluginInstalled() {
		t.Errorf("uninstall removed=%v err=%v installed=%v", removed, err, c.PluginInstalled())
	}
}

func TestPluginManifestOmitsDevVersion(t *testing.T) {
	raw, err := pluginManifest("dev")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if _, ok := m["version"]; ok {
		t.Errorf("dev build must not record a version: %s", raw)
	}
	if _, ok := m["skills"]; ok || m["name"] != "flow" {
		t.Errorf("manifest wrong: %s", raw)
	}
}

func TestPluginDisabled(t *testing.T) {
	for _, tc := range []struct {
		settings string
		want     bool
	}{
		{"", false},
		{`{}`, false},
		{`{"enabledPlugins":{"flow@skills-dir":true}}`, false},
		{`{"enabledPlugins":{"flow@skills-dir":false}}`, true},
		{`{"enabledPlugins":{"flow@market":false}}`, false},
		{`not json`, false},
	} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if tc.settings != "" {
			p := filepath.Join(home, ".claude", "settings.json")
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, []byte(tc.settings), 0o644)
		}
		if got := (&claude{}).PluginDisabled(); got != tc.want {
			t.Errorf("settings %q: PluginDisabled=%v, want %v", tc.settings, got, tc.want)
		}
	}
}
