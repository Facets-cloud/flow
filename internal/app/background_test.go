package app

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
	"flow/internal/harness/codex"
	"flow/internal/spawner"
)

// TestMain defaults claude.BGCommandRunner to an empty background-agent
// registry for the whole app test package. Without this, every show /
// list / do over a session-bound task would spawn a real
// `claude agents --json` subprocess (slow + non-hermetic). Tests that
// exercise bg behavior override it via stubBGCommand and restore on
// cleanup.
func TestMain(m *testing.M) {
	claude.BGCommandRunner = func(workDir string, args []string) ([]byte, error) {
		if len(args) >= 1 && args[0] == "agents" {
			return []byte("[]"), nil
		}
		return nil, fmt.Errorf("BGCommandRunner not stubbed for args %v", args)
	}
	// Insulate the suite from an ambient $FLOW_TERM=bg in the developer's
	// environment: default the whole app package to NON-background so
	// cmdDo tests exercise the terminal path. bg tests opt in via
	// stubBGMode (which saves/restores this override).
	// Hermetic harness presence: no harness binary counts as on PATH and
	// $CODEX_HOME never points at the developer's real Codex state, so
	// install tests only see harnesses their sandbox creates.
	harnessLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	// Never touch the developer's real tmux server: no tmux unless a
	// test stubs it in.
	codex.TmuxLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	codex.TmuxRunner = func(args []string) ([]byte, error) {
		return nil, fmt.Errorf("codex.TmuxRunner not stubbed for args %v", args)
	}
	os.Unsetenv("CODEX_HOME")
	// Workspace pre-trust writes Claude's config; never let an ambient
	// $CLAUDE_CONFIG_DIR point it at the developer's real one.
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	notBG := false
	spawner.BackgroundOverride = &notBG
	// Resumes are confirmed instantly in tests (one registry check, no
	// polling); tests of the confirmation window set it themselves.
	bgResumeConfirmTimeout = 0
	os.Exit(m.Run())
}

const bgFullSID = "48d287d9-1ef0-4738-84b9-3110beb988c4"

// bgBanner is what the stubbed `claude --bg` spawn returns: short id
// 48d287d9 (the prefix of bgFullSID).
const bgBanner = "backgrounded · 48d287d9 · flow/x\n"

// bgAgentsJSON builds a `claude agents --json` array of LIVE entries
// (pid set) — one per session id (short id = first 8 chars).
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

// bgAgentsJSONExited builds a registry entry for an exited session (no
// pid / status — `state` only), as `claude agents --json --all` reports a
// stopped/failed/done session whose process is gone.
func bgAgentsJSONExited(sid, state string) string {
	short := sid
	if len(short) >= 8 {
		short = short[:8]
	}
	return fmt.Sprintf(
		`[{"id":%q,"cwd":"/w","kind":"background","startedAt":1,"sessionId":%q,"name":"n","state":%q}]`,
		short, sid, state)
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
	spawn, resume, agents     int
	stop                      int
	lastSpawn, lastResume     []string
	lastStop                  []string
	lastResumeDir             string
	spawnBanner, resumeBanner string
	// onResume, if set, fires when a --resume command is issued — lets a
	// test mutate the registry so the resume's capture lookup resolves the
	// newly-forked id.
	onResume func()
	// onStop, if set, fires on `claude stop <id>`.
	onStop func()
	// onSpawn, if set, fires when a fresh (non --resume) spawn is issued.
	onSpawn func()
}

// stubBGCommand swaps claude.BGCommandRunner with a recorder that
// dispatches on args: `agents` returns *agentsJSON (mutable so a test can
// change registry state mid-run), `--resume` returns resumeBanner, the
// rest are spawns (returning spawnBanner). Both banners default to
// bgBanner; tests set resumeBanner to simulate the new id --bg mints on
// resume.
func stubBGCommand(t *testing.T, agentsJSON *string) *bgCalls {
	t.Helper()
	c := &bgCalls{spawnBanner: bgBanner, resumeBanner: bgBanner}
	old := claude.BGCommandRunner
	claude.BGCommandRunner = func(workDir string, args []string) ([]byte, error) {
		if len(args) >= 1 && args[0] == "agents" {
			c.agents++
			return []byte(*agentsJSON), nil
		}
		if len(args) >= 1 && args[0] == "stop" {
			c.stop++
			c.lastStop = args
			if c.onStop != nil {
				c.onStop()
			}
			return []byte("stopped " + args[len(args)-1] + "\n"), nil
		}
		for _, a := range args {
			if a == "--resume" {
				c.resume++
				c.lastResume = args
				c.lastResumeDir = workDir
				if c.onResume != nil {
					c.onResume()
				}
				return []byte(c.resumeBanner), nil
			}
		}
		c.spawn++
		c.lastSpawn = args
		if c.onSpawn != nil {
			c.onSpawn()
		}
		return []byte(c.spawnBanner), nil
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

// TestCmdDoBackgroundResumeWhenGone: a bound session ABSENT from the
// registry is brought back via --bg --resume <oldid>. flow records whatever
// id the resume captured — here the stub models a copy under a new id, so
// the NEW id (not the dead one) is re-recorded. (Live claude keeps the id
// for an unregistered session; see bg_nofork_test.go.)
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
	writeBoundTranscript(t, "bgt")

	// Old session removed; resume forks a new id (99aabbcc…), which must
	// be the only thing in the registry at capture time.
	const newSID = "99aabbcc-0000-4000-8000-000000000000"
	reg = bgAgentsJSON(newSID)
	c.resumeBanner = "backgrounded · 99aabbcc · bgt\n"

	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("resume cmdDo rc=%d", rc)
	}
	if c.spawn != 1 {
		t.Errorf("spawn calls = %d, want 1 (resume must not re-spawn fresh)", c.spawn)
	}
	if c.resume != 1 {
		t.Fatalf("resume calls = %d, want 1", c.resume)
	}
	if !pairArg(c.lastResume, "--resume", bgFullSID) {
		t.Errorf("resume must --resume the OLD id; argv: %v", c.lastResume)
	}
	if !pairArg(c.lastResume, "--name", "bgt") {
		t.Errorf("resume must keep the spawn display name (--name bgt); argv: %v", c.lastResume)
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "bgt")
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionID.String != newSID {
		t.Errorf("session_id after resume = %q, want re-recorded new id %q", task.SessionID.String, newSID)
	}
}

// TestCmdDoBackgroundResumeWhenExited: a bound session that's still listed
// but no longer running (exited, no pid) is resumed, not left as "already
// open". The stub models claude starting a copy; flow records the copy.
func TestCmdDoBackgroundResumeWhenExited(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bgt")
	stubBGMode(t)
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)

	if rc := cmdDo([]string{"bgt"}); rc != 0 { // fresh spawn, binds bgFullSID
		t.Fatalf("first cmdDo rc=%d", rc)
	}
	writeBoundTranscript(t, "bgt")

	// Session now present but EXITED (stopped, no pid): the alive-check must
	// see it as not-running → resume. The stub's resume yields a new id, so flip the
	// registry to that new session when the resume command fires (so the
	// capture lookup resolves it).
	const newSID = "77665544-0000-4000-8000-000000000000"
	reg = bgAgentsJSONExited(bgFullSID, "stopped")
	c.resumeBanner = "backgrounded · 77665544 · bgt\n"
	c.onResume = func() { reg = bgAgentsJSON(newSID) }

	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("resume cmdDo rc=%d", rc)
	}
	if c.resume != 1 {
		t.Fatalf("resume calls = %d, want 1 (exited session must resume)", c.resume)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "bgt")
	if task.SessionID.String != newSID {
		t.Errorf("session_id after resume = %q, want %q", task.SessionID.String, newSID)
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
	stubBGMode(t)
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
	stubBGMode(t)
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

// Regression guard: with FLOW_TERM unset (non-bg mode), flow show / list
// must NOT query the background-agent registry — no `claude agents`
// subprocess, no bg_status, no bg-sourced [live]. The TestMain default
// pins BackgroundOverride=false, so these run in non-bg mode.
func TestNonBgModeSkipsRegistry(t *testing.T) {
	bgCalled := false
	old := claude.BGCommandRunner
	claude.BGCommandRunner = func(workDir string, args []string) ([]byte, error) {
		if len(args) >= 1 && args[0] == "agents" {
			bgCalled = true
		}
		return []byte("[]"), nil
	}
	t.Cleanup(func() { claude.BGCommandRunner = old })
	stubPS(t, "")

	task := &flowdb.Task{
		Harness:   sql.NullString{String: "claude", Valid: true},
		SessionID: sql.NullString{String: bgFullSID, Valid: true},
	}
	if bgAgentStatus(task) != nil {
		t.Error("bgAgentStatus non-nil in non-bg mode")
	}
	_ = liveSessionsForTasks([]*flowdb.Task{task})
	if bgCalled {
		t.Error("non-bg mode must not run `claude agents` (latency regression)")
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

// resumeExitedWithMail drives the gap this test pair covers: a bg task
// whose session exited, with mail queued while nothing was live, is
// resumed. Returns the recorded resume argv.
func resumeExitedWithMail(t *testing.T, doArgs []string) []string {
	t.Helper()
	setupFlowRoot(t)
	t.Setenv(wakeOffEnv, "1")
	seedTask(t, "bgt")
	stubBGMode(t)
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)
	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("first cmdDo rc=%d", rc)
	}
	writeBoundTranscript(t, "bgt")
	db := openFlowDB(t)
	for _, id := range []string{"msgq0001", "msgq0002"} {
		if err := flowdb.InsertBusMessage(db, &flowdb.BusMessage{
			ID: id, CreatedAt: flowdb.NowISO(), Kind: "message",
			FromAssignee: "user", ToAssignee: "user", ToTaskSlug: "bgt",
			Body: "queued while no session was live",
		}); err != nil {
			t.Fatal(err)
		}
	}
	const newSID = "77665544-0000-4000-8000-000000000000"
	reg = bgAgentsJSONExited(bgFullSID, "stopped")
	c.resumeBanner = "backgrounded · 77665544 · bgt\n"
	c.onResume = func() { reg = bgAgentsJSON(newSID) }
	if rc := cmdDo(doArgs); rc != 0 {
		t.Fatalf("resume cmdDo rc=%d", rc)
	}
	if c.resume != 1 {
		t.Fatalf("resume calls = %d, want 1", c.resume)
	}
	return c.lastResume
}

// resumePrompt returns the positional prompt of a `claude --bg --resume`
// argv: the first arg that is neither a flag nor a flag's value.
func resumePrompt(argv []string) string {
	valued := map[string]bool{"--name": true, "--resume": true, "--settings": true}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if valued[a] {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		return a
	}
	return ""
}

// TestCmdDoBackgroundResumeKicksOff: a bg resume WITHOUT --with must still
// hand the session a prompt — with none, `claude --bg --resume` sits idle
// forever and mail queued while no session was live is never read. The
// kickoff re-loads task context, names the unread count, and drains the
// inbox. It names the slug (flow show task bgt): flow records the resumed
// session's (possibly new) id only after ResumeBackground returns, so a
// no-arg reverse lookup can race and find no bound task.
func TestCmdDoBackgroundResumeKicksOff(t *testing.T) {
	argv := resumeExitedWithMail(t, []string{"bgt"})
	p := resumePrompt(argv)
	if p == "" {
		t.Fatalf("bg resume passed no prompt; argv: %v", argv)
	}
	for _, want := range []string{"flow show task bgt", "flow inbox pop", "2 unread"} {
		if !strings.Contains(p, want) {
			t.Errorf("kickoff missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, harness.InjectionMarker) {
		t.Errorf("kickoff without --with must not carry the --with marker:\n%s", p)
	}
}

// TestCmdDoBackgroundResumeWithAppendsAfterKickoff: --with no longer
// replaces the kickoff; it rides behind the marker after it, exactly like
// a fresh bg spawn appends it to the bootstrap prompt.
func TestCmdDoBackgroundResumeWithAppendsAfterKickoff(t *testing.T) {
	argv := resumeExitedWithMail(t, []string{"--with", "also rebase on main", "bgt"})
	p := resumePrompt(argv)
	k := strings.Index(p, "flow inbox pop")
	m := strings.Index(p, harness.InjectionMarker+"\nalso rebase on main")
	if k < 0 || m < 0 || m < k {
		t.Errorf("want kickoff, then marker + --with text; got:\n%s", p)
	}
}

// writeBoundTranscript materializes the transcript of slug's bound
// session under its work_dir, so a bg re-run resumes rather than treating
// the session as never started.
func writeBoundTranscript(t *testing.T, slug string) {
	t.Helper()
	task, err := flowdb.GetTask(openFlowDB(t), slug)
	if err != nil {
		t.Fatal(err)
	}
	writeClaudeTranscript(t, task.WorkDir, task.SessionID.String)
}

// TestCmdDoBackgroundResumeWithoutTranscriptSpawnsFresh: a bound session
// with no transcript anywhere (e.g. a prompt-less bg resume that never
// took a turn) cannot be resumed — `claude --bg --resume` prints its
// banner and the agent then fails silently. flow must start a fresh
// background session instead, bootstrap prompt and queued mail included.
func TestCmdDoBackgroundResumeWithoutTranscriptSpawnsFresh(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv(wakeOffEnv, "1")
	seedTask(t, "bgt")
	stubBGMode(t)
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)
	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("first cmdDo rc=%d", rc)
	}
	db := openFlowDB(t)
	if err := flowdb.InsertBusMessage(db, &flowdb.BusMessage{
		ID: "msgq0003", CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: "user", ToAssignee: "user", ToTaskSlug: "bgt", Body: "queued",
	}); err != nil {
		t.Fatal(err)
	}
	const newSID = "77665544-0000-4000-8000-000000000000"
	reg = bgAgentsJSONExited(bgFullSID, "failed")
	c.spawnBanner = "backgrounded · 77665544 · bgt\n"
	c.onResume = func() { t.Fatalf("resumed a session with no transcript") }
	old := claude.BGCommandRunner
	claude.BGCommandRunner = func(workDir string, args []string) ([]byte, error) {
		if len(args) > 0 && args[0] != "agents" {
			reg = bgAgentsJSON(newSID)
		}
		return old(workDir, args)
	}
	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("cmdDo rc=%d", rc)
	}
	if c.resume != 0 || c.spawn != 2 {
		t.Fatalf("resume=%d spawn=%d, want a fresh spawn and no resume", c.resume, c.spawn)
	}
	p := c.lastSpawn[len(c.lastSpawn)-1]
	if !strings.Contains(p, "execution session for flow task bgt") || !strings.Contains(p, "1 unread") {
		t.Errorf("fresh spawn prompt lacks bootstrap or unread mail:\n%s", p)
	}
	task, _ := flowdb.GetTask(db, "bgt")
	if task.SessionID.String != newSID {
		t.Errorf("session_id = %q, want %q", task.SessionID.String, newSID)
	}
}

// markDoneNow simulates the spawned/resumed agent running `flow done` on
// itself before cmdDo records the session (the agent is a separate
// process that starts working as soon as the harness launches it).
// The done timestamp is a fixed sentinel so a test can prove the record
// left status_changed_at alone.
const concurrentDoneAt = "2001-02-03T04:05:06Z"

func markDoneNow(t *testing.T, slug string) {
	t.Helper()
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET status='done', status_changed_at=? WHERE slug=?`, concurrentDoneAt, slug); err != nil {
		t.Fatal(err)
	}
}

// assertConcurrentDoneKept checks the record kept the agent's done (status
// and its timestamp) and still bound the session the agent runs under.
func assertConcurrentDoneKept(t *testing.T, slug, wantSID string) {
	t.Helper()
	var st string
	var sid, changed sql.NullString
	if err := openFlowDB(t).QueryRow(`SELECT status, session_id, status_changed_at FROM tasks WHERE slug=?`, slug).Scan(&st, &sid, &changed); err != nil {
		t.Fatal(err)
	}
	if st != "done" {
		t.Errorf("status = %q, want done (the record must not reopen a task the agent closed)", st)
	}
	if changed.String != concurrentDoneAt {
		t.Errorf("status_changed_at = %q, want the agent's done time %q", changed.String, concurrentDoneAt)
	}
	if sid.String != wantSID {
		t.Errorf("session_id = %q, want the new session %q", sid.String, wantSID)
	}
}

// TestCmdDoBackgroundResumeKeepsConcurrentDone: if the resumed agent runs
// `flow done` before flow records the resume, the record must not reopen
// the task — it still captures the session id the agent runs under.
func TestCmdDoBackgroundResumeKeepsConcurrentDone(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv(wakeOffEnv, "1")
	seedTask(t, "bgt")
	stubBGMode(t)
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)
	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("first cmdDo rc=%d", rc)
	}
	writeBoundTranscript(t, "bgt")
	// claude brings it back under a NEW id (the copy path), so the test
	// proves the record still binds the new session after the done.
	const newSID = "77665544-0000-4000-8000-000000000000"
	reg = bgAgentsJSONExited(bgFullSID, "stopped")
	c.resumeBanner = "backgrounded · 77665544 · bgt\n"
	c.onResume = func() {
		reg = bgAgentsJSON(newSID)
		markDoneNow(t, "bgt")
	}
	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("resume cmdDo rc=%d", rc)
	}
	assertConcurrentDoneKept(t, "bgt", newSID)
}

// TestCmdDoBackgroundFreshSpawnKeepsConcurrentDone: same race on a fresh
// background spawn. Only reachable with --fresh on a bound task: a
// never-bound task cannot be marked done (status CHECK needs a session).
func TestCmdDoBackgroundFreshSpawnKeepsConcurrentDone(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bgt")
	stubBGMode(t)
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)
	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("first cmdDo rc=%d", rc)
	}
	const newSID = "99887766-0000-4000-8000-000000000000"
	reg = bgAgentsJSONExited(bgFullSID, "stopped")
	c.spawnBanner = "backgrounded · 99887766 · bgt\n"
	c.onSpawn = func() {
		reg = bgAgentsJSON(newSID)
		markDoneNow(t, "bgt")
	}
	if rc := cmdDo([]string{"--fresh", "bgt"}); rc != 0 {
		t.Fatalf("cmdDo rc=%d", rc)
	}
	assertConcurrentDoneKept(t, "bgt", newSID)
}
