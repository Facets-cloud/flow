package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Claude hooks ship as a skills-dir plugin inside ~/.claude/skills/flow/.
// These tests pin the install/uninstall contract, parity with flowHooks,
// and the legacy-hook guard.

func claudePluginPaths(home string) (manifest, hooks string) {
	d := filepath.Join(home, ".claude", "skills", "flow")
	return filepath.Join(d, ".claude-plugin", "plugin.json"), filepath.Join(d, "hooks", "hooks.json")
}

// readPluginHooks returns the "hooks" map from the plugin's hooks.json.
func readPluginHooks(t *testing.T, home string) map[string]any {
	t.Helper()
	_, hp := claudePluginPaths(home)
	m := readSettings(t, hp)
	hooks, _ := m["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatalf("hooks.json has no hooks key: %v", m)
	}
	return hooks
}

func TestSkillInstallWritesPluginManifest(t *testing.T) {
	home := withTempHome(t)
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	mp, _ := claudePluginPaths(home)
	m := readSettings(t, mp)
	if m["name"] != "flow" {
		t.Errorf("plugin name = %v, want flow", m["name"])
	}
	if _, ok := m["skills"]; ok {
		t.Errorf("plugin.json must not carry a skills key (it would namespace the skill as flow:flow): %v", m)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "flow", "skills")); !os.IsNotExist(err) {
		t.Errorf("no skills/ subdir may exist in the plugin dir; stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "flow", "SKILL.md")); err != nil {
		t.Errorf("root SKILL.md missing: %v", err)
	}
}

func TestSkillInstallPluginManifestVersion(t *testing.T) {
	home := withTempHome(t)
	old := Version
	Version = "v1.2.3"
	t.Cleanup(func() { Version = old })
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	mp, _ := claudePluginPaths(home)
	if v := readSettings(t, mp)["version"]; v != "1.2.3" {
		t.Errorf("plugin version = %v, want 1.2.3", v)
	}
}

// Every flowHooks entry and the rewake waiter appear in hooks.json exactly
// once with --src plugin, under the matcher the legacy entries used.
func TestPluginHooksMatchFlowHooks(t *testing.T) {
	home := withTempHome(t)
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	hooks := readPluginHooks(t, home)
	wantMatcher := map[string]string{
		"SessionStart":     "startup|resume|compact|fork",
		"PreToolUse":       "Bash|Edit|Write|Read|Glob|Grep|Agent|WebFetch|WebSearch",
		"UserPromptSubmit": "",
		"Stop":             "",
	}
	type want struct{ event, cmd string }
	var wants []want
	for _, fh := range flowHooks() {
		wants = append(wants, want{fh.event, fh.command + " --src plugin"})
	}
	if len(wants) != 4 {
		t.Fatalf("flowHooks has %d entries; update this test's matcher table", len(wants))
	}
	for _, w := range wants {
		entries, _ := hooks[w.event].([]any)
		if got := countMatchingHookEntries(entries, w.cmd); got != 1 {
			t.Errorf("%s: %d entries running %q, want 1 (%v)", w.event, got, w.cmd, entries)
		}
		for _, e := range entries {
			m := e.(map[string]any)
			if mt, _ := m["matcher"].(string); mt != wantMatcher[w.event] {
				t.Errorf("%s: matcher %q, want %q", w.event, mt, wantMatcher[w.event])
			}
		}
	}
	for _, ev := range []string{"SessionStart", "Stop"} {
		entries, _ := hooks[ev].([]any)
		found := 0
		for _, e := range entries {
			for _, h := range e.(map[string]any)["hooks"].([]any) {
				hm := h.(map[string]any)
				if hm["command"] == rewakeHookCommand+" --src plugin" {
					found++
					if hm["asyncRewake"] != true || hm["timeout"] != float64(86400) || hm["rewakeMessage"] != rewakeMessage {
						t.Errorf("%s: rewake hook lost its async fields: %v", ev, hm)
					}
				}
			}
		}
		if found != 1 {
			t.Errorf("%s: %d rewake hooks, want 1", ev, found)
		}
	}
	// Nothing beyond the live set (no retired PostToolUse).
	for ev := range hooks {
		if _, ok := wantMatcher[ev]; !ok {
			t.Errorf("unexpected hook event %q in hooks.json", ev)
		}
	}
	for _, ev := range []string{"UserPromptSubmit", "PreToolUse"} {
		if n := len(hooks[ev].([]any)); n != 1 {
			t.Errorf("%s: %d entries, want 1", ev, n)
		}
	}
}

// The headline guarantee: installing never creates or rewrites
// ~/.claude/settings.json — not on a clean HOME, not over an existing
// file (legacy flow entries included: this release keeps them).
func TestSkillInstallDoesNotTouchSettingsJSON(t *testing.T) {
	t.Run("clean home", func(t *testing.T) {
		home := withTempHome(t)
		for _, args := range [][]string{{"install"}, {"install", "--force"}, {"update"}} {
			if rc := cmdSkill(args); rc != 0 {
				t.Fatalf("%v rc=%d", args, rc)
			}
		}
		if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
			t.Errorf("settings.json must not exist; stat err=%v", err)
		}
	})
	t.Run("existing settings with legacy flow hooks", func(t *testing.T) {
		home := withTempHome(t)
		sp := filepath.Join(home, ".claude", "settings.json")
		_ = os.MkdirAll(filepath.Dir(sp), 0o755)
		before := []byte(legacySettingsFixture)
		if err := os.WriteFile(sp, before, 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"install"}, {"update"}} {
			if rc := cmdSkill(args); rc != 0 {
				t.Fatalf("%v rc=%d", args, rc)
			}
		}
		maybeAutoUpgradeSkill()
		after, _ := os.ReadFile(sp)
		if !bytes.Equal(before, after) {
			t.Errorf("settings.json changed:\nbefore %s\nafter  %s", before, after)
		}
	})
}

func TestSkillInstallPluginIsIdempotent(t *testing.T) {
	home := withTempHome(t)
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	_, hp := claudePluginPaths(home)
	first, _ := os.ReadFile(hp)
	if rc := cmdSkill([]string{"install", "--force"}); rc != 0 {
		t.Fatalf("install --force rc=%d", rc)
	}
	second, _ := os.ReadFile(hp)
	if !bytes.Equal(first, second) {
		t.Errorf("hooks.json changed on reinstall:\n%s\n%s", first, second)
	}
}

func TestSkillInstallSkipHook(t *testing.T) {
	home := withTempHome(t)
	if rc := cmdSkill([]string{"install", "--skip-hook"}); rc != 0 {
		t.Fatalf("install --skip-hook rc=%d", rc)
	}
	mp, hp := claudePluginPaths(home)
	for _, p := range []string{mp, hp, filepath.Join(home, ".claude", "settings.json")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("--skip-hook must not write %s; stat err=%v", p, err)
		}
	}
}

// legacySettingsFixture is a settings.json as a pre-plugin flow left it,
// with old matchers, the retired PostToolUse hook, the rewake waiter and
// unrelated user hooks/keys.
const legacySettingsFixture = `{
  "hooks": {
    "SessionStart": [
      {"matcher": "startup|resume", "hooks": [{"type": "command", "command": "flow hook session-start"}]},
      {"matcher": "startup|resume|compact|fork", "hooks": [{"type": "command", "command": "flow inbox wait --rewake", "asyncRewake": true, "timeout": 86400, "rewakeMessage": "flow bus:"}]}
    ],
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": "my-custom-tool --watch"}]},
      {"hooks": [{"type": "command", "command": "flow hook user-prompt-submit"}]}
    ],
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "user-pretool-script"}]},
      {"matcher": "Bash|Edit|Write|Read|Glob|Grep|Agent|WebFetch|WebSearch", "hooks": [{"type": "command", "command": "flow hook pre-tool-use"}]}
    ],
    "PostToolUse": [
      {"matcher": "Bash|Edit", "hooks": [{"type": "command", "command": "flow hook post-tool-use"}]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "flow hook stop"}, {"type": "command", "command": "user-stop"}]},
      {"hooks": [{"type": "command", "command": "flow inbox wait --rewake", "asyncRewake": true, "timeout": 86400, "rewakeMessage": "flow bus:"}]}
    ]
  },
  "enabledPlugins": {"other@market": true},
  "someUnrelatedKey": "preserve-me"
}
`

// The strip (gated off for install in this release) removes every flow
// entry — live set, old matchers, PostToolUse, rewake — and nothing else.
func TestMigrateLegacyHooksRemovesAll(t *testing.T) {
	home := withTempHome(t)
	sp := filepath.Join(home, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(sp), 0o755)
	if err := os.WriteFile(sp, []byte(legacySettingsFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := harnessByName("claude")
	if err != nil {
		t.Fatal(err)
	}
	if n := migrateLegacyHooks(h, true); n != 6 {
		t.Errorf("removed %d hook kinds, want 6 (4 live + PostToolUse + rewake)", n)
	}
	raw, _ := os.ReadFile(sp)
	if strings.Contains(string(raw), "flow hook") || strings.Contains(string(raw), "flow inbox") {
		t.Errorf("flow entries left behind: %s", raw)
	}
	s := readSettings(t, sp)
	hooks, _ := s["hooks"].(map[string]any)
	for ev, cmd := range map[string]string{"UserPromptSubmit": "my-custom-tool --watch", "PreToolUse": "user-pretool-script", "Stop": "user-stop"} {
		if !hookEventReferencesCommand(hooks, ev, cmd) {
			t.Errorf("%s: user hook %q must be preserved: %v", ev, cmd, hooks[ev])
		}
	}
	if s["someUnrelatedKey"] != "preserve-me" || s["enabledPlugins"] == nil {
		t.Errorf("unrelated keys must be preserved: %v", s)
	}
	if n := migrateLegacyHooks(h, true); n != 0 {
		t.Errorf("second strip removed %d, want 0", n)
	}
}

// With the gate on, install strips the legacy entries.
func TestInstallStripsLegacyWhenGateOn(t *testing.T) {
	home := withTempHome(t)
	old := stripLegacyClaudeHooks
	stripLegacyClaudeHooks = true
	t.Cleanup(func() { stripLegacyClaudeHooks = old })
	sp := filepath.Join(home, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(sp), 0o755)
	_ = os.WriteFile(sp, []byte(legacySettingsFixture), 0o644)
	if rc := cmdSkill([]string{"install", "--harness", "claude"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	raw, _ := os.ReadFile(sp)
	if strings.Contains(string(raw), "flow hook") {
		t.Errorf("gate on: legacy entries should be stripped: %s", raw)
	}
}

func TestUninstallRemovesPluginAndLegacy(t *testing.T) {
	home := withTempHome(t)
	sp := filepath.Join(home, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(sp), 0o755)
	_ = os.WriteFile(sp, []byte(legacySettingsFixture), 0o644)
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	if rc := cmdSkill([]string{"uninstall"}); rc != 0 {
		t.Fatalf("uninstall rc=%d", rc)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "flow")); !os.IsNotExist(err) {
		t.Errorf("skill/plugin dir should be gone; stat err=%v", err)
	}
	raw, _ := os.ReadFile(sp)
	if strings.Contains(string(raw), "flow hook") || strings.Contains(string(raw), "flow inbox") {
		t.Errorf("uninstall left legacy flow hooks: %s", raw)
	}
	if !strings.Contains(string(raw), "user-stop") {
		t.Errorf("uninstall removed a user hook: %s", raw)
	}
}

func TestUninstallFreshInstallNeverCreatesSettings(t *testing.T) {
	home := withTempHome(t)
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	if rc := cmdSkill([]string{"uninstall"}); rc != 0 {
		t.Fatalf("uninstall rc=%d", rc)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Errorf("uninstall must not create settings.json; stat err=%v", err)
	}
}

func TestUninstallKeepHookKeepsPlugin(t *testing.T) {
	home := withTempHome(t)
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	if rc := cmdSkill([]string{"uninstall", "--keep-hook", "--harness", "claude"}); rc != 0 {
		t.Fatalf("uninstall rc=%d", rc)
	}
	mp, hp := claudePluginPaths(home)
	for _, p := range []string{mp, hp} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("--keep-hook must keep %s: %v", p, err)
		}
	}
	d := filepath.Join(home, ".claude", "skills", "flow")
	for _, p := range []string{"SKILL.md", "references", "VERSION"} {
		if _, err := os.Stat(filepath.Join(d, p)); !os.IsNotExist(err) {
			t.Errorf("--keep-hook should remove %s; stat err=%v", p, err)
		}
	}
}

// A user who ran `claude plugin disable flow@skills-dir` keeps it disabled:
// install refreshes the files, says so, and never touches enabledPlugins.
func TestSkillInstallRespectsDisabledPlugin(t *testing.T) {
	home := withTempHome(t)
	sp := filepath.Join(home, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(sp), 0o755)
	before := []byte(`{"enabledPlugins": {"flow@skills-dir": false}}` + "\n")
	_ = os.WriteFile(sp, before, 0o644)
	out := captureStdout(t, func() {
		if rc := cmdSkill([]string{"install", "--harness", "claude"}); rc != 0 {
			t.Fatalf("install rc=%d", rc)
		}
	})
	if !strings.Contains(out, "disabled") {
		t.Errorf("install should note the disabled plugin; out=%s", out)
	}
	after, _ := os.ReadFile(sp)
	if !bytes.Equal(before, after) {
		t.Errorf("settings.json changed: %s", after)
	}
	if claudePluginActive() {
		t.Error("claudePluginActive should be false for a disabled plugin")
	}
}

// ---------- legacy-hook guard ----------

// guardEnv sets up a Claude hook process environment with the plugin
// installed and a short start-time wait.
func guardEnv(t *testing.T) {
	t.Helper()
	withTempHome(t)
	t.Setenv("FLOW_ROOT", t.TempDir())
	t.Setenv("CLAUDE_PID", "1")
	old := legacyStartWait
	legacyStartWait = 150 * time.Millisecond
	t.Cleanup(func() { legacyStartWait = old })
	if rc := cmdSkill([]string{"install", "--harness", "claude"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
}

func setHookSession(t *testing.T, sid string) {
	t.Helper()
	old := hookSessionID
	hookSessionID = sid
	t.Cleanup(func() { hookSessionID = old })
}

func TestLegacyHookNoopsWhenPluginMarker(t *testing.T) {
	guardEnv(t)
	setHookSession(t, "sess-a")
	if hookSuperseded("", false) {
		t.Fatal("no marker yet: legacy hook must run")
	}
	if hookSuperseded(hookSrcPlugin, true) {
		t.Fatal("a plugin hook is never superseded")
	}
	if !pluginMarked("sess-a") {
		t.Fatal("plugin hook should record the session marker")
	}
	if !hookSuperseded("", false) || !hookSuperseded("", true) {
		t.Error("marker present: legacy hooks must no-op")
	}
	setHookSession(t, "sess-b")
	if hookSuperseded("", false) {
		t.Error("another session without a marker must still run legacy hooks")
	}
}

// At a session start the legacy and plugin SessionStart run in parallel:
// the legacy one waits briefly for the plugin's marker.
func TestLegacySessionStartWaitsForPluginMarker(t *testing.T) {
	guardEnv(t)
	setHookSession(t, "sess-c")
	go func() {
		time.Sleep(30 * time.Millisecond)
		markPluginSession("sess-c", false)
	}()
	if !hookSuperseded("", true) {
		t.Error("legacy start hook should stand down once the plugin marker lands")
	}
	// No plugin hook ever runs (e.g. plugins blocked): run after the wait.
	setHookSession(t, "sess-d")
	start := time.Now()
	if hookSuperseded("", true) {
		t.Error("no marker within the wait: legacy hook must run")
	}
	if time.Since(start) < legacyStartWait {
		t.Error("legacy start hook should have waited for the marker")
	}
}

// Without the plugin installed (or when disabled) a start never waits.
func TestLegacyHookNoWaitWithoutPlugin(t *testing.T) {
	withTempHome(t)
	t.Setenv("FLOW_ROOT", t.TempDir())
	t.Setenv("CLAUDE_PID", "1")
	old := legacyStartWait
	legacyStartWait = 5 * time.Second
	t.Cleanup(func() { legacyStartWait = old })
	setHookSession(t, "sess-e")
	start := time.Now()
	if hookSuperseded("", true) {
		t.Error("no plugin: legacy hook must run")
	}
	if time.Since(start) > time.Second {
		t.Error("no plugin: must not wait")
	}
}

// Codex/prx hooks (no Claude env) are never superseded, marker or not.
func TestLegacyGuardIgnoresNonClaude(t *testing.T) {
	guardEnv(t)
	t.Setenv("CLAUDE_PID", "")
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "")
	setHookSession(t, "sess-f")
	markPluginSession("sess-f", false)
	if hookSuperseded("", true) {
		t.Error("a non-Claude hook process must never be superseded")
	}
}

// End to end through the handler: with the marker present the legacy
// SessionStart handler prints nothing; the plugin one still emits its
// context (here the unbound-session skill hint).
func TestHookHandlersHonorGuard(t *testing.T) {
	guardEnv(t)
	payload := `{"session_id":"sess-g","source":"startup","hook_event_name":"SessionStart"}`
	markPluginSession("sess-g", false)
	var rc int
	out := captureStdout(t, func() {
		runWithStdin(t, payload, func() { rc = cmdHook([]string{"session-start"}) })
	})
	if rc != 0 || strings.TrimSpace(out) != "" {
		t.Errorf("legacy handler with marker: rc=%d out=%q, want silent 0", rc, out)
	}
	out = captureStdout(t, func() {
		runWithStdin(t, payload, func() { rc = cmdHook([]string{"session-start", "--src", "plugin"}) })
	})
	if rc != 0 || strings.TrimSpace(out) == "" {
		t.Errorf("plugin handler: rc=%d out=%q, want context", rc, out)
	}
	for _, sub := range []string{"user-prompt-submit", "pre-tool-use", "stop"} {
		if rc := runHookWithStdin(t, payload, sub, "--src", "plugin"); rc != 0 {
			t.Errorf("%s --src plugin rc=%d", sub, rc)
		}
	}
	// The rewake waiter accepts --src too (and exits quietly here).
	runWithStdin(t, payload, func() { rc = cmdInboxWait([]string{"--rewake", "--src", "plugin", "--timeout", "1"}) })
	if rc != 0 {
		t.Errorf("inbox wait --rewake --src plugin rc=%d", rc)
	}
}

// runWithStdin runs fn with os.Stdin fed from payload.
func runWithStdin(t *testing.T, payload string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString(payload)
	_ = w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; _ = r.Close() }()
	fn()
}

func runHookWithStdin(t *testing.T, payload string, args ...string) int {
	t.Helper()
	var rc int
	runWithStdin(t, payload, func() { rc = cmdHook(args) })
	return rc
}
