package app

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"flow/internal/flowdb"
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
// once in plugin form (FLOW_HOOK_SRC=plugin prefix), under the matcher the legacy entries used.
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
		wants = append(wants, want{fh.event, "FLOW_HOOK_SRC=plugin " + fh.command})
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
				if hm["command"] == "FLOW_HOOK_SRC=plugin "+rewakeHookCommand {
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

// The headline guarantee: installing never creates ~/.claude/settings.json
// and never adds to it. Over a legacy file it changes nothing while a live
// session still runs on those legacy hooks (the strip waits; see
// TestAutoStrip*).
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
		t.Setenv("FLOW_ROOT", t.TempDir())
		stubLiveClaude(t, "legacy-sess")
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

// ---------- duplicate-copy claim guard ----------

// stubLiveClaude pins the live Claude session ids the legacy strip sees.
func stubLiveClaude(t *testing.T, ids ...string) {
	t.Helper()
	old := liveClaudeSessions
	liveClaudeSessions = func() (map[string]int, error) {
		m := map[string]int{}
		for _, id := range ids {
			m[id] = 1
		}
		return m, nil
	}
	t.Cleanup(func() { liveClaudeSessions = old })
}

// guardEnv sets up a Claude hook process environment with the plugin
// installed and a legacy settings.json kept (a live legacy session).
func guardEnv(t *testing.T) string {
	t.Helper()
	home := withTempHome(t)
	t.Setenv("FLOW_ROOT", t.TempDir())
	t.Setenv("CLAUDE_PID", "1")
	stubLiveClaude(t, "some-legacy-session")
	sp := filepath.Join(home, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(sp), 0o755)
	_ = os.WriteFile(sp, []byte(legacySettingsFixture), 0o644)
	if rc := cmdSkill([]string{"install", "--harness", "claude"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	if legacyStripDone() {
		t.Fatal("strip must wait for the live legacy session")
	}
	return home
}

// setHook sets the decoded-payload globals a handler sees.
func setHook(t *testing.T, sid, raw string) {
	t.Helper()
	oldS, oldR := hookSessionID, hookRawPayload
	hookSessionID, hookRawPayload = sid, []byte(raw)
	t.Cleanup(func() { hookSessionID, hookRawPayload = oldS, oldR })
}

// Both copies of one invocation race: exactly one wins, every time.
func TestClaimRaceExactlyOneRuns(t *testing.T) {
	guardEnv(t)
	for i := 0; i < 50; i++ {
		payload := []byte(fmt.Sprintf(`{"session_id":"s","n":%d}`, i))
		var wg sync.WaitGroup
		var wins atomic.Int32
		for c := 0; c < 2; c++ {
			wg.Add(1)
			go func(c int) {
				defer wg.Done()
				var won bool
				if i%2 == 0 {
					won = claim("stop", "s", payload)
				} else {
					won = claimHandle("pre-tool-use", "s", fmt.Sprintf("toolu_%d", i))
				}
				if won {
					wins.Add(1)
				}
			}(c)
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("iteration %d: %d copies ran, want exactly 1", i, wins.Load())
		}
	}
}

// Whichever copy (plugin or legacy) arrives first runs; the other no-ops.
func TestClaimBothOrders(t *testing.T) {
	guardEnv(t)
	for _, order := range [][2]string{{hookSrcPlugin, ""}, {"", hookSrcPlugin}} {
		raw := fmt.Sprintf(`{"session_id":"s1","hook_event_name":"Stop","order":%q}`, order[0])
		setHook(t, "s1", raw)
		p := hookPayload{SessionID: "s1"}
		if hookLoses("stop", order[0], p) {
			t.Errorf("order %q first: the first copy must run", order)
		}
		if !hookLoses("stop", order[1], p) {
			t.Errorf("order %q first: the second copy must no-op", order)
		}
	}
	if !sessionOnPlugin("s1") {
		t.Error("a --src plugin copy must record the session as on plugin")
	}
}

// Handle claims key on the native id (tool_use_id, prompt_id), not the
// payload; hash claims key on the payload; handlers never share claims.
func TestClaimHandleVsHashKeys(t *testing.T) {
	guardEnv(t)
	setHook(t, "s2", `{"a":1}`)
	if hookLoses("pre-tool-use", "", hookPayload{ToolUseID: "toolu_1"}) {
		t.Fatal("first pre-tool-use copy must run")
	}
	hookRawPayload = []byte(`{"a":2}`) // different bytes, same tool_use_id
	if !hookLoses("pre-tool-use", hookSrcPlugin, hookPayload{ToolUseID: "toolu_1"}) {
		t.Error("same tool_use_id must lose even with different payload bytes")
	}
	if hookLoses("pre-tool-use", "", hookPayload{ToolUseID: "toolu_2"}) {
		t.Error("a new tool_use_id must run")
	}
	if hookLoses("user-prompt-submit", "", hookPayload{PromptID: "p1"}) ||
		!hookLoses("user-prompt-submit", hookSrcPlugin, hookPayload{PromptID: "p1"}) {
		t.Error("user-prompt-submit must key on prompt_id")
	}
	// Hash claims: same handler + bytes lose; other bytes or handler run.
	hookRawPayload = []byte(`{"x":1}`)
	if hookLoses("session-start", "", hookPayload{}) || !hookLoses("session-start", hookSrcPlugin, hookPayload{}) {
		t.Error("identical session-start payloads: first runs, second loses")
	}
	if hookLoses("rewake", "", hookPayload{}) {
		t.Error("rewake must not share session-start's claim")
	}
	hookRawPayload = []byte(`{"x":2}`)
	if hookLoses("session-start", "", hookPayload{}) {
		t.Error("a different payload must run")
	}
	// Other session, same bytes: runs.
	setHook(t, "s3", `{"x":2}`)
	if hookLoses("session-start", "", hookPayload{}) {
		t.Error("another session must run")
	}
}

// The hash window lets a genuinely repeated identical event run again;
// handle claims have no window.
func TestClaimWindowExpiry(t *testing.T) {
	guardEnv(t)
	old := claimWindow
	claimWindow = 80 * time.Millisecond
	t.Cleanup(func() { claimWindow = old })
	payload := []byte(`{"session_id":"s4","prompt":"same again"}`)
	if !claim("user-prompt-submit", "s4", payload) || claim("user-prompt-submit", "s4", payload) {
		t.Fatal("first claim wins, immediate duplicate loses")
	}
	time.Sleep(100 * time.Millisecond)
	if !claim("user-prompt-submit", "s4", payload) {
		t.Error("an identical prompt after the window must run")
	}
	if !claimHandle("pre-tool-use", "s4", "toolu_9") {
		t.Fatal("handle claim")
	}
	time.Sleep(100 * time.Millisecond)
	if claimHandle("pre-tool-use", "s4", "toolu_9") {
		t.Error("handle claims have no window")
	}
}

// Codex/prx hooks (no Claude env) never claim, so never lose; after the
// strip there is one copy and the claim is skipped too.
func TestClaimSkippedOutsideClaudeAndAfterStrip(t *testing.T) {
	guardEnv(t)
	setHook(t, "s5", `{"y":1}`)
	t.Setenv("CLAUDE_PID", "")
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "")
	if hookLoses("stop", "", hookPayload{}) || hookLoses("stop", "", hookPayload{}) {
		t.Error("a non-Claude hook process must never lose")
	}
	t.Setenv("CLAUDE_PID", "1")
	recordLegacyStripDone()
	if hookLoses("stop", "", hookPayload{}) || hookLoses("stop", hookSrcPlugin, hookPayload{}) {
		t.Error("after the strip no claim is taken")
	}
}

// End to end through the handlers: the copy that runs second is silent.
func TestHookHandlersHonorGuard(t *testing.T) {
	guardEnv(t)
	run := func(payload string, args ...string) (int, string) {
		var rc int
		out := captureStdout(t, func() { runWithStdin(t, payload, func() { rc = cmdHook(args) }) })
		return rc, strings.TrimSpace(out)
	}
	p1 := `{"session_id":"sess-g","source":"startup","hook_event_name":"SessionStart"}`
	if rc, out := run(p1, "session-start", "--src", "plugin"); rc != 0 || out == "" {
		t.Errorf("plugin copy first: rc=%d out=%q, want context", rc, out)
	}
	if rc, out := run(p1, "session-start"); rc != 0 || out != "" {
		t.Errorf("legacy copy second: rc=%d out=%q, want silent 0", rc, out)
	}
	p2 := `{"session_id":"sess-h","source":"startup","hook_event_name":"SessionStart"}`
	if rc, out := run(p2, "session-start"); rc != 0 || out == "" {
		t.Errorf("legacy copy first: rc=%d out=%q, want context", rc, out)
	}
	if rc, out := run(p2, "session-start", "--src", "plugin"); rc != 0 || out != "" {
		t.Errorf("plugin copy second: rc=%d out=%q, want silent 0", rc, out)
	}
	if !sessionOnPlugin("sess-h") {
		t.Error("a losing plugin copy still proves the session is on plugin")
	}
	for _, sub := range []string{"user-prompt-submit", "pre-tool-use", "stop"} {
		if rc := runHookWithStdin(t, p1, sub, "--src", "plugin"); rc != 0 {
			t.Errorf("%s --src plugin rc=%d", sub, rc)
		}
	}
	var rc int
	runWithStdin(t, p1, func() { rc = cmdInboxWait([]string{"--rewake", "--src", "plugin", "--timeout", "1"}) })
	if rc != 0 {
		t.Errorf("inbox wait --rewake --src plugin rc=%d", rc)
	}
}

// ---------- legacy strip ----------

func legacySettings(t *testing.T, home string) string {
	t.Helper()
	raw, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	return string(raw)
}

// The automatic strip waits while a live session is not on plugin, then
// strips once it is (or has ended), records done, and leaves user hooks.
func TestAutoStripWaitsForLiveLegacySession(t *testing.T) {
	home := guardEnv(t)
	t.Setenv(selfUpgradeEnv, "")
	old := legacyStripEvery
	legacyStripEvery = 0
	t.Cleanup(func() { legacyStripEvery = old })

	stubLiveClaude(t, "legacy-1", "plugin-1")
	markPluginSession("plugin-1")
	maybeStripLegacyHooks()
	if !strings.Contains(legacySettings(t, home), `"flow hook stop"`) || legacyStripDone() {
		t.Fatal("legacy-1 is live and not on plugin: must not strip")
	}
	markPluginSession("LEGACY-1") // ids compare case-insensitively
	maybeStripLegacyHooks()
	got := legacySettings(t, home)
	if strings.Contains(got, `"flow hook`) || strings.Contains(got, `"flow inbox wait --rewake"`) {
		t.Errorf("every live session on plugin: legacy entries should be stripped: %s", got)
	}
	if !strings.Contains(got, "user-stop") || !legacyStripDone() {
		t.Errorf("strip must keep user hooks and record done: %s", got)
	}
}

func TestAutoStripWhenLegacySessionEnded(t *testing.T) {
	home := guardEnv(t)
	t.Setenv(selfUpgradeEnv, "")
	stubLiveClaude(t) // the legacy session is gone
	old := legacyStripEvery
	legacyStripEvery = time.Hour
	t.Cleanup(func() { legacyStripEvery = old })
	// guardEnv's install touched no throttle stamp: the first check runs.
	maybeStripLegacyHooks()
	if strings.Contains(legacySettings(t, home), `"flow hook`) {
		t.Error("no live legacy session: should strip")
	}
}

// The throttle: within legacyStripEvery the check is skipped.
func TestAutoStripThrottled(t *testing.T) {
	home := guardEnv(t)
	t.Setenv(selfUpgradeEnv, "")
	old := legacyStripEvery
	legacyStripEvery = time.Hour
	t.Cleanup(func() { legacyStripEvery = old })
	maybeStripLegacyHooks() // live legacy session: checked, kept
	stubLiveClaude(t)
	maybeStripLegacyHooks() // throttled
	if !strings.Contains(legacySettings(t, home), `"flow hook stop"`) {
		t.Error("a second check within the throttle window must not run")
	}
}

// The automatic check honors FLOW_NO_AUTO_UPGRADE and the plugin being
// disabled.
func TestAutoStripSkips(t *testing.T) {
	home := guardEnv(t)
	stubLiveClaude(t)
	old := legacyStripEvery
	legacyStripEvery = 0
	t.Cleanup(func() { legacyStripEvery = old })
	t.Setenv(selfUpgradeEnv, "1")
	maybeStripLegacyHooks()
	if !strings.Contains(legacySettings(t, home), `"flow hook stop"`) {
		t.Error("FLOW_NO_AUTO_UPGRADE must stop the automatic strip")
	}
	t.Setenv(selfUpgradeEnv, "")
	sp := filepath.Join(home, ".claude", "settings.json")
	disabled := strings.Replace(legacySettingsFixture, `"other@market": true`, `"other@market": true, "flow@skills-dir": false`, 1)
	_ = os.WriteFile(sp, []byte(disabled), 0o644)
	maybeStripLegacyHooks()
	if !strings.Contains(legacySettings(t, home), `"flow hook stop"`) {
		t.Error("a disabled plugin must keep the legacy hooks")
	}
}

// --strip-legacy forces the strip despite a live legacy session.
func TestSkillUpdateStripLegacyForces(t *testing.T) {
	home := guardEnv(t)
	out := captureStdout(t, func() {
		if rc := cmdSkill([]string{"update", "--harness", "claude"}); rc != 0 {
			t.Fatalf("update rc=%d", rc)
		}
	})
	if !strings.Contains(out, "kept flow's legacy hooks") || !strings.Contains(legacySettings(t, home), `"flow hook stop"`) {
		t.Fatalf("without the flag the live legacy session keeps them; out=%s", out)
	}
	out = captureStdout(t, func() {
		if rc := cmdSkill([]string{"update", "--harness", "claude", "--strip-legacy"}); rc != 0 {
			t.Fatalf("update --strip-legacy rc=%d", rc)
		}
	})
	if strings.Contains(legacySettings(t, home), `"flow hook`) || !legacyStripDone() {
		t.Errorf("--strip-legacy must strip and record done; out=%s settings=%s", out, legacySettings(t, home))
	}
}

// The strip never runs inside a hook: Run skips it for hook/inbox.
func TestRunSkipsStripInHooks(t *testing.T) {
	home := guardEnv(t)
	t.Setenv(selfUpgradeEnv, "")
	stubLiveClaude(t)
	old := legacyStripEvery
	legacyStripEvery = 0
	t.Cleanup(func() { legacyStripEvery = old })
	runWithStdin(t, `{"session_id":"z","hook_event_name":"Stop"}`, func() { Run([]string{"hook", "stop"}) })
	if !strings.Contains(legacySettings(t, home), `"flow hook stop"`) {
		t.Error("a hook invocation must not strip")
	}
	captureStdout(t, func() { Run([]string{"list", "tasks"}) })
	if strings.Contains(legacySettings(t, home), `"flow hook stop"`) {
		t.Error("an ordinary command should run the strip check")
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

// The plugin form marks its source via FLOW_HOOK_SRC (an env prefix an
// older binary ignores), not only via --src.
func TestHookSrcFromEnv(t *testing.T) {
	guardEnv(t)
	setHook(t, "s-env", `{"e":1}`)
	t.Setenv(hookSrcEnv, hookSrcPlugin)
	if hookLoses("stop", "", hookPayload{}) {
		t.Fatal("first copy must run")
	}
	if !sessionOnPlugin("s-env") {
		t.Error("FLOW_HOOK_SRC=plugin must record the session as on plugin")
	}
}

// Legacy entries that reappear (an older flow re-installed them) clear the
// done record, so the guard claims again until the next strip.
func TestLegacyEntriesReappearClearDone(t *testing.T) {
	home := withTempHome(t)
	t.Setenv("FLOW_ROOT", t.TempDir())
	stubLiveClaude(t, "legacy-x")
	if rc := cmdSkill([]string{"install", "--harness", "claude"}); rc != 0 {
		t.Fatal("install")
	}
	if !legacyStripDone() {
		t.Fatal("a clean install has nothing to strip: done")
	}
	sp := filepath.Join(home, ".claude", "settings.json")
	_ = os.WriteFile(sp, []byte(legacySettingsFixture), 0o644)
	if rc := cmdSkill([]string{"update", "--harness", "claude"}); rc != 0 {
		t.Fatal("update")
	}
	if legacyStripDone() {
		t.Error("reappeared legacy entries must clear the done record")
	}
}

// ---------- strip announcement ----------

// stripAnnounceEnv: an initialized flow root + DB, the plugin installed,
// legacy entries present, no live Claude session, checks unthrottled.
func stripAnnounceEnv(t *testing.T) string {
	t.Helper()
	initTempFlowRoot(t)
	t.Setenv(selfUpgradeEnv, "")
	t.Setenv("CLAUDE_PID", "")
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "")
	stubLiveClaude(t)
	old := legacyStripEvery
	legacyStripEvery = 0
	t.Cleanup(func() { legacyStripEvery = old })
	captureStdout(t, func() {
		if rc := cmdInit([]string{"--harness", "claude"}); rc != 0 {
			t.Fatalf("init rc=%d", rc)
		}
	})
	home := os.Getenv("HOME")
	sp := filepath.Join(home, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(sp), 0o755)
	_ = os.WriteFile(sp, []byte(legacySettingsFixture), 0o644)
	_ = os.Remove(legacyStripDonePath()) // as if upgraded over a legacy install
	return home
}

// stripFYIs returns the unread human FYIs carrying the strip notice.
func stripFYIs(t *testing.T) []string {
	t.Helper()
	db, err := openBusDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ms, err := flowdb.UnreadForHuman(db, busUser)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range ms {
		if strings.Contains(m.Body, "hooks moved to the flow plugin") {
			if !m.FYI {
				t.Errorf("strip notice must be an FYI: %+v", m)
			}
			out = append(out, m.Body)
		}
	}
	return out
}

func TestLegacyStripAnnouncesOnce(t *testing.T) {
	home := stripAnnounceEnv(t)
	before := time.Now()
	stderr := stderrOf(t, maybeStripLegacyHooks)
	if strings.Contains(legacySettings(t, home), `"flow hook`) {
		t.Fatal("expected the strip to run")
	}
	want := legacyStripNotice(before)
	if strings.Count(stderr, "hooks moved to the flow plugin") != 1 ||
		!strings.Contains(stderr, "restart any Claude sessions opened before ") {
		t.Errorf("want exactly one notice line on stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, want) && !strings.Contains(stderr, legacyStripNotice(time.Now())) {
		t.Errorf("notice should carry the strip time (HH:MM); got %q want %q", stderr, want)
	}
	if fyis := stripFYIs(t); len(fyis) != 1 || !strings.Contains(stderr, fyis[0]) {
		t.Errorf("want one human FYI with the same text, got %v", fyis)
	}
	// Re-runs (automatic and explicit) never repeat it.
	stderr = stderrOf(t, func() {
		maybeStripLegacyHooks()
		captureStdoutOnly(t, func() { cmdSkill([]string{"update", "--harness", "claude"}) })
		captureStdoutOnly(t, func() { cmdSkill([]string{"update", "--harness", "claude", "--strip-legacy"}) })
	})
	if strings.Contains(stderr, "hooks moved") {
		t.Errorf("re-run repeated the notice: %q", stderr)
	}
	if n := len(stripFYIs(t)); n != 1 {
		t.Errorf("re-run added FYIs: %d, want 1", n)
	}
}

func TestLegacyStripForcedAnnounces(t *testing.T) {
	stripAnnounceEnv(t)
	stubLiveClaude(t, "still-on-legacy")
	stderr := stderrOf(t, func() {
		captureStdoutOnly(t, func() { cmdSkill([]string{"update", "--harness", "claude", "--strip-legacy"}) })
	})
	if strings.Count(stderr, "hooks moved to the flow plugin") != 1 || len(stripFYIs(t)) != 1 {
		t.Errorf("--strip-legacy must announce once; stderr=%q fyis=%v", stderr, stripFYIs(t))
	}
}

// Nothing stripped → no line, no FYI: a clean install, a live legacy
// session, and the hook path.
func TestLegacyStripSilentWhenNothingStripped(t *testing.T) {
	t.Run("clean install", func(t *testing.T) {
		home := stripAnnounceEnv(t)
		_ = os.Remove(filepath.Join(home, ".claude", "settings.json"))
		stderr := stderrOf(t, func() {
			maybeStripLegacyHooks()
			captureStdoutOnly(t, func() { cmdSkill([]string{"update", "--harness", "claude"}) })
		})
		if strings.Contains(stderr, "hooks moved") || len(stripFYIs(t)) != 0 {
			t.Errorf("nothing to strip must be silent; stderr=%q", stderr)
		}
	})
	t.Run("live legacy session", func(t *testing.T) {
		stripAnnounceEnv(t)
		stubLiveClaude(t, "still-on-legacy")
		stderr := stderrOf(t, maybeStripLegacyHooks)
		if strings.Contains(stderr, "hooks moved") || len(stripFYIs(t)) != 0 {
			t.Errorf("a kept strip must be silent; stderr=%q", stderr)
		}
	})
	t.Run("hook path", func(t *testing.T) {
		home := stripAnnounceEnv(t)
		inHookPath = true
		t.Cleanup(func() { inHookPath = false })
		stderr := stderrOf(t, func() {
			captureStdoutOnly(t, func() { cmdSkill([]string{"update", "--harness", "claude", "--strip-legacy"}) })
		})
		if strings.Contains(stderr, "hooks moved") || len(stripFYIs(t)) != 0 ||
			!strings.Contains(legacySettings(t, home), `"flow hook stop"`) {
			t.Errorf("no strip and no notice inside a hook; stderr=%q", stderr)
		}
	})
}
