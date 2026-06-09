package app

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
	"flow/internal/spawner"
)

// TestMain defaults claude.BGCommandRunner to an empty background-agent
// registry for the whole app test package. Without this, every show /
// list / do over a session-bound task would spawn a real
// `claude agents --json` subprocess (slow + non-hermetic). Tests that
// exercise bg behavior override it via stubBGCommand and restore on
// cleanup.
func TestMain(m *testing.M) {
	claude.BGCommandRunner = func(args []string) ([]byte, error) {
		if len(args) >= 1 && args[0] == "agents" {
			return []byte("[]"), nil
		}
		return nil, fmt.Errorf("BGCommandRunner not stubbed for args %v", args)
	}
	os.Exit(m.Run())
}

const bgFullSID = "48d287d9-1ef0-4738-84b9-3110beb988c4"

// bgBanner is what the stubbed `claude --bg` spawn returns: short id
// 48d287d9 (the prefix of bgFullSID).
const bgBanner = "backgrounded · 48d287d9 · flow/x\n"

// bgAgentsJSON builds a `claude agents --json` array containing one entry
// per session id (short id = first 8 chars).
func bgAgentsJSON(sids ...string) string {
	var b strings.Builder
	b.WriteString("[")
	for i, sid := range sids {
		if i > 0 {
			b.WriteString(",")
		}
		short := sid
		if len(short) >= 8 {
			short = short[:8]
		}
		fmt.Fprintf(&b,
			`{"pid":%d,"id":%q,"cwd":"/w","kind":"background","startedAt":1,"sessionId":%q,"name":"n","status":"busy","state":"working"}`,
			1000+i, short, sid)
	}
	b.WriteString("]")
	return b.String()
}

// stubBGMode forces spawner.IsBackground() true for the test.
func stubBGMode(t *testing.T) {
	t.Helper()
	tru := true
	old := spawner.BackgroundOverride
	spawner.BackgroundOverride = &tru
	t.Cleanup(func() { spawner.BackgroundOverride = old })
}

type bgCalls struct {
	spawn, resume, agents int
	lastSpawn, lastResume []string
}

// stubBGCommand swaps claude.BGCommandRunner with a recorder that
// dispatches on args: `agents --json` returns *agentsJSON (mutable so a
// test can change registry state mid-run), `--resume` is a resume, the
// rest are spawns (returning bgBanner).
func stubBGCommand(t *testing.T, agentsJSON *string) *bgCalls {
	t.Helper()
	c := &bgCalls{}
	old := claude.BGCommandRunner
	claude.BGCommandRunner = func(args []string) ([]byte, error) {
		if len(args) >= 1 && args[0] == "agents" {
			c.agents++
			return []byte(*agentsJSON), nil
		}
		for _, a := range args {
			if a == "--resume" {
				c.resume++
				c.lastResume = args
				return []byte(""), nil
			}
		}
		c.spawn++
		c.lastSpawn = args
		return []byte(bgBanner), nil
	}
	t.Cleanup(func() { claude.BGCommandRunner = old })
	return c
}

// TestCmdDoBackgroundFreshSpawn: $FLOW_TERM=bg flow do <task> spawns a bg
// agent, captures the REAL full session id (not a pre-allocated phantom),
// records it + harness, flips to in-progress, and opens NO terminal tab.
func TestCmdDoBackgroundFreshSpawn(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bgt")
	stubBGMode(t)
	itermCount, _ := stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)

	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("cmdDo (bg) rc=%d, want 0", rc)
	}
	if *itermCount != 0 {
		t.Errorf("terminal spawn count = %d, want 0 (bg opens no tab)", *itermCount)
	}
	if c.spawn != 1 {
		t.Fatalf("bg spawn calls = %d, want 1", c.spawn)
	}
	if !containsArg(c.lastSpawn, "--bg") || !pairArg(c.lastSpawn, "--name", "bgt") {
		t.Errorf("spawn argv wrong: %v", c.lastSpawn)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "bgt")
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionID.String != bgFullSID {
		t.Errorf("session_id = %q, want captured %q", task.SessionID.String, bgFullSID)
	}
	if task.Status != "in-progress" {
		t.Errorf("status = %q, want in-progress", task.Status)
	}
	if !task.Harness.Valid || task.Harness.String != "claude" {
		t.Errorf("harness = %+v, want claude", task.Harness)
	}
}

// TestCmdDoBackgroundAlreadyRunning: re-running while the session is alive
// in the registry must NOT spawn or resume — just report.
func TestCmdDoBackgroundAlreadyRunning(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bgt")
	stubBGMode(t)
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)

	if rc := cmdDo([]string{"bgt"}); rc != 0 { // fresh spawn, binds bgFullSID
		t.Fatalf("first cmdDo rc=%d", rc)
	}
	if rc := cmdDo([]string{"bgt"}); rc != 0 { // session still alive
		t.Fatalf("second cmdDo rc=%d", rc)
	}
	if c.spawn != 1 {
		t.Errorf("spawn calls = %d, want 1 (no re-spawn when already running)", c.spawn)
	}
	if c.resume != 0 {
		t.Errorf("resume calls = %d, want 0 (already running, not gone)", c.resume)
	}
}

// TestCmdDoBackgroundResumeWhenGone: a bound session absent from the
// registry is resumed (same id), not re-spawned.
func TestCmdDoBackgroundResumeWhenGone(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bgt")
	stubBGMode(t)
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)

	if rc := cmdDo([]string{"bgt"}); rc != 0 { // fresh spawn, binds bgFullSID
		t.Fatalf("first cmdDo rc=%d", rc)
	}
	reg = "[]" // session no longer running
	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("resume cmdDo rc=%d", rc)
	}
	if c.spawn != 1 {
		t.Errorf("spawn calls = %d, want 1 (resume must not re-spawn)", c.spawn)
	}
	if c.resume != 1 {
		t.Fatalf("resume calls = %d, want 1", c.resume)
	}
	if !pairArg(c.lastResume, "--resume", bgFullSID) {
		t.Errorf("resume argv wrong: %v", c.lastResume)
	}
}

// nonBGHarness satisfies harness.Harness (via embedded interface) but NOT
// harness.BackgroundLauncher, so it exercises the capability gate.
type nonBGHarness struct{ harness.Harness }

func (nonBGHarness) Name() harness.Name { return harness.Name("codex") }

func TestBackgroundLauncherForGate(t *testing.T) {
	if _, err := backgroundLauncherFor(claude.New()); err != nil {
		t.Errorf("claude must implement BackgroundLauncher: %v", err)
	}
	_, err := backgroundLauncherFor(nonBGHarness{})
	if err == nil {
		t.Fatalf("non-bg harness must error (clean capability gate)")
	}
	if !strings.Contains(err.Error(), "codex") {
		t.Errorf("gate error should name the harness, got: %v", err)
	}
}

// TestBGAgentStatus: a claude-bound task whose session is in the registry
// resolves to its live agent (status/state/pid); absent or session-less
// tasks resolve to nil.
func TestBGAgentStatus(t *testing.T) {
	reg := bgAgentsJSON(bgFullSID)
	stubBGCommand(t, &reg)

	live := &flowdb.Task{
		Slug:      "x",
		Harness:   sql.NullString{String: "claude", Valid: true},
		SessionID: sql.NullString{String: bgFullSID, Valid: true},
	}
	a := bgAgentStatus(live)
	if a == nil {
		t.Fatal("bgAgentStatus = nil, want the live agent")
	}
	if a.Status != "busy" || a.State != "working" || a.PID == 0 {
		t.Errorf("agent fields = %+v, want busy/working with a pid", *a)
	}

	absent := &flowdb.Task{
		Harness:   sql.NullString{String: "claude", Valid: true},
		SessionID: sql.NullString{String: "ffffffff-0000-4000-8000-000000000000", Valid: true},
	}
	if bgAgentStatus(absent) != nil {
		t.Error("bgAgentStatus for absent session = non-nil, want nil")
	}

	noSession := &flowdb.Task{Harness: sql.NullString{String: "claude", Valid: true}}
	if bgAgentStatus(noSession) != nil {
		t.Error("bgAgentStatus for session-less task = non-nil, want nil")
	}
}

// TestLiveSessionsIncludesBackgroundAgents: the [live] detection used by
// `flow list` must count background agents (which don't show in the ps
// scan) as live.
func TestLiveSessionsIncludesBackgroundAgents(t *testing.T) {
	stubPS(t, "") // nothing live via the process table
	reg := bgAgentsJSON(bgFullSID)
	stubBGCommand(t, &reg)

	tasks := []*flowdb.Task{{
		Harness:   sql.NullString{String: "claude", Valid: true},
		SessionID: sql.NullString{String: bgFullSID, Valid: true},
	}}
	merged := liveSessionsForTasks(tasks)
	if merged[strings.ToLower(bgFullSID)] == 0 {
		t.Errorf("background agent not counted as live: %v", merged)
	}
}

// TestCmdShowRendersBackgroundStatus: `flow show` surfaces a live bg
// session's status/state/pid via the per-render registry lookup.
func TestCmdShowRendersBackgroundStatus(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bgt")
	stubBGMode(t)
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	stubBGCommand(t, &reg)

	if rc := cmdDo([]string{"bgt"}); rc != 0 { // bind a bg session
		t.Fatalf("bg spawn rc=%d", rc)
	}
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "bgt"}); rc != 0 {
			t.Errorf("show rc=%d", rc)
		}
	})
	if !strings.Contains(out, "bg_status:") {
		t.Fatalf("flow show missing bg_status line:\n%s", out)
	}
	if !strings.Contains(out, "busy") || !strings.Contains(out, "working") {
		t.Errorf("bg_status missing status/state:\n%s", out)
	}
}

// ---- helpers ----

func containsArg(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func pairArg(ss []string, flag, val string) bool {
	for i := 0; i+1 < len(ss); i++ {
		if ss[i] == flag && ss[i+1] == val {
			return true
		}
	}
	return false
}
