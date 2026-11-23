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
	"flow/internal/harness"
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
	if !strings.Contains(legacySettings(t, home), `"flow hook stop"`) {
		t.Fatal("install must never strip on its own")
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

// stripAnnounceEnv: an initialized flow root + DB, the plugin installed,
// legacy entries present, no live Claude session, a Claude hook process.
func stripAnnounceEnv(t *testing.T) string {
	t.Helper()
	initTempFlowRoot(t)
	t.Setenv(selfUpgradeEnv, "")
	t.Setenv("CLAUDE_PID", "")
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "")
	t.Setenv(hookSrcEnv, "")
	stubLiveClaude(t)
	captureStdout(t, func() {
		if rc := cmdInit([]string{"--harness", "claude"}); rc != 0 {
			t.Fatalf("init rc=%d", rc)
		}
	})
	home := os.Getenv("HOME")
	sp := filepath.Join(home, ".claude", "settings.json")
	_ = os.MkdirAll(filepath.Dir(sp), 0o755)
	_ = os.WriteFile(sp, []byte(legacySettingsFixture), 0o644)
	t.Setenv("CLAUDE_PID", "1")
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

// sessionStart runs `flow hook session-start` for sid; plugin selects the
// plugin copy. Returns stdout and stderr.
func sessionStart(t *testing.T, sid string, plugin bool) (string, string) {
	t.Helper()
	args := []string{"session-start"}
	if plugin {
		args = append(args, "--src", "plugin")
	}
	payload := fmt.Sprintf(`{"session_id":%q,"source":"startup","hook_event_name":"SessionStart"}`, sid)
	var out string
	stderr := stderrOf(t, func() {
		out = captureStdoutOnly(t, func() { runHookWithStdin(t, payload, args...) })
	})
	return strings.TrimSpace(out), stderr
}

func assertLegacyKept(t *testing.T, home, why string) {
	t.Helper()
	if !strings.Contains(legacySettings(t, home), `"flow hook stop"`) {
		t.Errorf("%s: legacy entries must be kept; settings=%s", why, legacySettings(t, home))
	}
}

func assertLegacyStripped(t *testing.T, home string) {
	t.Helper()
	got := legacySettings(t, home)
	if strings.Contains(got, `"flow hook`) || strings.Contains(got, `"flow inbox wait --rewake"`) {
		t.Errorf("legacy entries should be stripped: %s", got)
	}
	for _, keep := range []string{"user-stop", "my-custom-tool --watch", "user-pretool-script", "preserve-me", "other@market"} {
		if !strings.Contains(got, keep) {
			t.Errorf("strip must keep %q: %s", keep, got)
		}
	}
}

// Plugin-only fresh start, no other session live: the plugin's
// SessionStart strips once and leaves one FYI; nothing on the hook's
// stderr; the session still gets its context.
func TestAutoStripFromPluginSessionStart(t *testing.T) {
	home := stripAnnounceEnv(t)
	stubLiveClaude(t, "sess-p") // only the session firing the hook
	out, stderr := sessionStart(t, "sess-p", true)
	if out == "" {
		t.Error("the plugin SessionStart must still emit its context")
	}
	assertLegacyStripped(t, home)
	if strings.Contains(stderr, "hooks moved") {
		t.Errorf("a hook must not print the notice to stderr: %q", stderr)
	}
	fyis := stripFYIs(t)
	if len(fyis) != 1 || !strings.Contains(fyis[0], "sess-p") || !strings.Contains(fyis[0], "Nothing to restart") {
		t.Fatalf("want one FYI naming the triggering session, got %v", fyis)
	}
	// Later starts (and forced updates) never repeat it.
	sessionStart(t, "sess-q", true)
	captureStdoutOnly(t, func() { cmdSkill([]string{"update", "--harness", "claude", "--strip-legacy"}) })
	if n := len(stripFYIs(t)); n != 1 {
		t.Errorf("re-runs added FYIs: %d, want 1", n)
	}
}

// The plugin copy strips even when the legacy copy won the claim.
func TestAutoStripWhenLegacyCopyWonClaim(t *testing.T) {
	home := stripAnnounceEnv(t)
	stubLiveClaude(t, "sess-w")
	if out, _ := sessionStart(t, "sess-w", false); out == "" {
		t.Fatal("legacy copy first: it runs")
	}
	if out, _ := sessionStart(t, "sess-w", true); out != "" {
		t.Errorf("plugin copy second must be silent, got %q", out)
	}
	assertLegacyStripped(t, home)
}

// Another session flow can see is live: no strip, no FYI — even if that
// session is itself on the plugin.
func TestAutoStripWaitsForOtherLiveSession(t *testing.T) {
	home := stripAnnounceEnv(t)
	stubLiveClaude(t, "sess-p", "other")
	markPluginSession("other")
	sessionStart(t, "sess-p", true)
	assertLegacyKept(t, home, "another live session")
	if len(stripFYIs(t)) != 0 {
		t.Error("no strip, no FYI")
	}
	stubLiveClaude(t, "sess-p2") // the other session ended
	sessionStart(t, "sess-p2", true)
	assertLegacyStripped(t, home)
}

// The plugin never fires (old Claude, policy, plugin disabled anywhere):
// only legacy copies run. They fire once each, and nothing — hooks,
// ordinary commands, install/update — ever strips.
func TestNoStripWhenPluginNeverFires(t *testing.T) {
	home := stripAnnounceEnv(t)
	for _, sid := range []string{"legacy-a", "legacy-b"} {
		if out, _ := sessionStart(t, sid, false); out == "" {
			t.Errorf("%s: the lone legacy SessionStart must run (fire once)", sid)
		}
	}
	for _, sub := range []string{"user-prompt-submit", "pre-tool-use", "stop"} {
		runHookWithStdin(t, `{"session_id":"legacy-a","hook_event_name":"X","prompt_id":"p1","tool_use_id":"t1"}`, sub)
	}
	captureStdoutOnly(t, func() { Run([]string{"list", "tasks"}) })
	captureStdoutOnly(t, func() { cmdSkill([]string{"update", "--harness", "claude"}) })
	captureStdoutOnly(t, func() { cmdSkill([]string{"install", "--harness", "claude", "--force"}) })
	assertLegacyKept(t, home, "plugin never fired")
	if sessionOnPlugin("legacy-a") || len(stripFYIs(t)) != 0 {
		t.Error("no plugin marker, no FYI")
	}
	// A second legacy copy of the same event still loses (no doubling if
	// the plugin later loads alongside).
	setHook(t, "legacy-c", `{"z":1}`)
	if hookLoses("stop", "", hookPayload{HookEventName: "Stop"}) || !hookLoses("stop", hookSrcPlugin, hookPayload{HookEventName: "Stop"}) {
		t.Error("guard must stay on while legacy entries exist")
	}
}

// FLOW_NO_AUTO_UPGRADE stops the automatic strip.
func TestAutoStripHonorsNoAutoUpgrade(t *testing.T) {
	home := stripAnnounceEnv(t)
	t.Setenv(selfUpgradeEnv, "1")
	stubLiveClaude(t, "sess-n")
	sessionStart(t, "sess-n", true)
	assertLegacyKept(t, home, "FLOW_NO_AUTO_UPGRADE")
}

// Concurrent plugin session starts strip and announce exactly once.
func TestAutoStripConcurrentOnce(t *testing.T) {
	home := stripAnnounceEnv(t)
	stubLiveClaude(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			stripLegacyClaudeHooks(false, fmt.Sprintf("c-%d", i))
		}(i)
	}
	wg.Wait()
	assertLegacyStripped(t, home)
	if n := len(stripFYIs(t)); n != 1 {
		t.Errorf("want exactly one FYI, got %d", n)
	}
}

// --strip-legacy forces the strip despite a live session; without it,
// update keeps the entries and says how they will go.
func TestSkillUpdateStripLegacyForces(t *testing.T) {
	home := stripAnnounceEnv(t)
	stubLiveClaude(t, "still-on-legacy")
	out := captureStdoutOnly(t, func() {
		if rc := cmdSkill([]string{"update", "--harness", "claude"}); rc != 0 {
			t.Fatalf("update rc=%d", rc)
		}
	})
	if !strings.Contains(out, "kept flow's legacy hooks") {
		t.Errorf("update should explain the kept entries; out=%s", out)
	}
	assertLegacyKept(t, home, "update without the flag")
	stderr := stderrOf(t, func() {
		captureStdoutOnly(t, func() {
			if rc := cmdSkill([]string{"update", "--harness", "claude", "--strip-legacy"}); rc != 0 {
				t.Fatalf("update --strip-legacy rc=%d", rc)
			}
		})
	})
	assertLegacyStripped(t, home)
	if strings.Count(stderr, "hooks moved to the flow plugin") != 1 || !strings.Contains(stderr, "--strip-legacy") {
		t.Errorf("a forced strip prints one stderr line, got %q", stderr)
	}
	if fyis := stripFYIs(t); len(fyis) != 1 || !strings.Contains(stderr, fyis[0]) {
		t.Errorf("want one FYI with the stderr text, got %v", fyis)
	}
}

// A failed plugin install never strips, even with --strip-legacy.
func TestStripLegacyRefusedWhenPluginInstallFails(t *testing.T) {
	home := stripAnnounceEnv(t)
	hooksDir := filepath.Join(home, ".claude", "skills", "flow", "hooks")
	_ = os.RemoveAll(hooksDir)
	if err := os.WriteFile(hooksDir, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	stderrOf(t, func() {
		captureStdoutOnly(t, func() { cmdSkill([]string{"update", "--harness", "claude", "--strip-legacy"}) })
	})
	assertLegacyKept(t, home, "plugin install failed")
}

// An unreadable settings.json is "could not look", not "clean": no strip,
// and the guard keeps claiming.
func TestSettingsReadErrorIsNotClean(t *testing.T) {
	home := stripAnnounceEnv(t)
	sp := filepath.Join(home, ".claude", "settings.json")
	_ = os.Remove(sp)
	_ = os.MkdirAll(sp, 0o755) // a directory: ReadFile fails
	if _, err := legacyClaudeEntriesPresent(); err == nil {
		t.Fatal("want a read error")
	}
	if ok, why := stripLegacyClaudeHooks(true, ""); ok || !strings.Contains(why, "cannot read") {
		t.Errorf("strip on a read error: ok=%v why=%q", ok, why)
	}
	setHook(t, "s-err", `{"r":1}`)
	if hookLoses("stop", "", hookPayload{HookEventName: "Stop"}) || !hookLoses("stop", hookSrcPlugin, hookPayload{HookEventName: "Stop"}) {
		t.Error("guard must claim when settings.json can't be read")
	}
}

// A legacy copy already in flight when the strip lands still loses its
// claim (the grace window), so a late Stop cannot undo the winner.
func TestGuardSpansStripTransition(t *testing.T) {
	home := stripAnnounceEnv(t)
	setHook(t, "s-t", `{"stop":1}`)
	p := hookPayload{HookEventName: "Stop"}
	if hookLoses("stop", hookSrcPlugin, p) {
		t.Fatal("first copy runs")
	}
	if ok, _ := stripLegacyClaudeHooks(true, ""); !ok {
		t.Fatal("strip")
	}
	assertLegacyStripped(t, home)
	if !hookLoses("stop", "", p) {
		t.Error("the in-flight legacy copy must still lose after the strip")
	}
	// After the grace window: one copy only, no claims taken.
	old := legacyStripGrace
	legacyStripGrace = 0
	t.Cleanup(func() { legacyStripGrace = old })
	if hookLoses("stop", "", p) || hookLoses("stop", hookSrcPlugin, p) {
		t.Error("no claims once settings.json is clean and the grace passed")
	}
}

// Downgrade/rollback: an older flow re-adds the settings entries after a
// strip; the guard sees them on its next call and claims again.
func TestGuardReArmsWhenLegacyEntriesReappear(t *testing.T) {
	home := stripAnnounceEnv(t)
	stripLegacyClaudeHooks(true, "")
	old := legacyStripGrace
	legacyStripGrace = 0
	t.Cleanup(func() { legacyStripGrace = old })
	setHook(t, "s-d", `{"d":1}`)
	p := hookPayload{HookEventName: "Stop"}
	if hookLoses("stop", "", p) || hookLoses("stop", "", p) {
		t.Fatal("clean settings: never claims")
	}
	_ = os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(legacySettingsFixture), 0o644)
	if hookLoses("stop", "", p) || !hookLoses("stop", hookSrcPlugin, p) {
		t.Error("reappeared legacy entries: the guard must claim again")
	}
}

// Claude processes claim by env or by hook_event_name in the payload (a
// Claude release that drops CLAUDE_PID still dedups); a manual run with
// neither never claims.
func TestClaimDetection(t *testing.T) {
	guardEnv(t)
	t.Setenv("CLAUDE_PID", "")
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "")
	setHook(t, "s5", `{"y":1}`)
	if hookLoses("stop", "", hookPayload{}) || hookLoses("stop", "", hookPayload{}) {
		t.Error("no Claude env and no hook_event_name: never loses")
	}
	setHook(t, "s6", `{"y":2}`)
	p := hookPayload{HookEventName: "Stop"}
	if hookLoses("stop", "", p) || !hookLoses("stop", hookSrcPlugin, p) {
		t.Error("hook_event_name alone must enable the claim")
	}
}

// The live check counts background agents whatever the backend, and a
// slow `claude agents` is "unknown", never "none".
type slowBG struct {
	harness.BackgroundLauncher
	delay time.Duration
}

func (s slowBG) BackgroundAgents() ([]harness.BackgroundAgent, error) {
	time.Sleep(s.delay)
	return []harness.BackgroundAgent{{SessionID: "bg-1", PID: 42}}, nil
}

func TestBackgroundAgentsWithin(t *testing.T) {
	if a, err := backgroundAgentsWithin(slowBG{delay: 0}, time.Second); err != nil || len(a) != 1 {
		t.Errorf("fast: %v %v", a, err)
	}
	if _, err := backgroundAgentsWithin(slowBG{delay: 200 * time.Millisecond}, 20*time.Millisecond); err == nil {
		t.Error("a timeout must be an error (strip waits)")
	}
}
