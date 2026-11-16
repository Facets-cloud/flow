package app

import (
	"strings"
	"testing"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness/claude"
)

// Claude's keep-vs-fork rules for `claude --bg --resume <id>` (verified
// live on claude 2.1.325, see the PR body for the evidence):
//
//	session state                      flags passed   result
//	alive (any status)                 any            COPY, new id
//	registered, exited (stopped/done)  none           SAME id, saved options
//	registered, exited                 any            COPY, new id
//	unregistered (rm'd / never bg)     any            SAME id
//
// flow therefore resumes a registered session with no flags, delivers to an
// alive idle session instead of resuming it, and only ever forks as a last
// resort.

func noFlags(t *testing.T, argv []string) {
	t.Helper()
	for _, a := range argv {
		if a == "--name" || a == "--dangerously-skip-permissions" || a == "--settings" {
			t.Errorf("registered resume must pass no launch flags (they force a copy); argv: %v", argv)
			return
		}
	}
}

// An exited session that still has a background record comes back under
// the SAME id: flow passes no flags, so claude wakes it with its saved
// options (name, permissions) instead of starting a copy.
func TestCmdDoBackgroundResumeRegisteredKeepsSameID(t *testing.T) {
	c, reg := spawnBound(t)
	setConfirmTimeout(t, 0)
	*reg = bgAgentsJSONExited(bgFullSID, "stopped")
	c.resumeBanner = "note: woke session 48d287d9 with its saved options (--name).\nbackgrounded · 48d287d9 · bgt\n"
	c.onResume = func() { *reg = "[" + bgAgentLive(bgFullSID, 4242, "busy", "working") + "]" }
	if rc := cmdDo([]string{"--dangerously-skip-permissions", "bgt"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if c.resume != 1 || !pairArg(c.lastResume, "--resume", bgFullSID) {
		t.Fatalf("resume=%d argv=%v, want --resume %s", c.resume, c.lastResume, bgFullSID)
	}
	noFlags(t, c.lastResume)
	if resumePrompt(c.lastResume) == "" {
		t.Errorf("registered resume lost its kickoff prompt: %v", c.lastResume)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "bgt")
	if task.SessionID.String != bgFullSID {
		t.Errorf("session_id = %q, want the same id %q", task.SessionID.String, bgFullSID)
	}
	if _, ok := flowdb.SessionSource(db, bgFullSID); ok {
		t.Error("lineage recorded for a same-id resume")
	}
}

// An unregistered session (removed from the Agent View, or never a bg
// session) keeps its id even with flags, so flow still names it.
func TestCmdDoBackgroundResumeUnregisteredKeepsName(t *testing.T) {
	c, reg := spawnBound(t)
	setConfirmTimeout(t, 0)
	*reg = "[]"
	c.onResume = func() { *reg = bgAgentsJSON(bgFullSID) }
	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !pairArg(c.lastResume, "--name", "bgt") {
		t.Errorf("unregistered resume should keep --name; argv: %v", c.lastResume)
	}
	task, _ := flowdb.GetTask(openFlowDB(t), "bgt")
	if task.SessionID.String != bgFullSID {
		t.Errorf("session_id = %q, want %q", task.SessionID.String, bgFullSID)
	}
}

// Alive and idle: resuming would only start a copy. Deliver the
// instruction to the running session through its wake channel instead.
func TestCmdDoBackgroundIdleAliveDeliversWithViaWake(t *testing.T) {
	c, reg := spawnBound(t)
	f := &fakeWaker{live: true}
	stubWaker(t, f)
	*reg = "[" + bgAgentLive(bgFullSID, 4242, "idle", "done") + "]"
	if rc := cmdDo([]string{"--with", "rebase on main", "bgt"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if c.resume != 0 || c.stop != 0 || c.spawn != 1 {
		t.Fatalf("resume=%d stop=%d spawn=%d, want delivery only", c.resume, c.stop, c.spawn)
	}
	if len(f.pushes) != 1 || !strings.Contains(f.pushes[0].Notice, "rebase on main") ||
		f.pushes[0].SessionID != bgFullSID {
		t.Fatalf("pushes = %+v, want one push of the --with text to %s", f.pushes, bgFullSID)
	}
	task, _ := flowdb.GetTask(openFlowDB(t), "bgt")
	if task.SessionID.String != bgFullSID {
		t.Errorf("session_id = %q, want unchanged", task.SessionID.String)
	}
}

// Queued mail is also something to deliver to an idle live session.
func TestCmdDoBackgroundIdleAliveDeliversMailNotice(t *testing.T) {
	c, reg := spawnBound(t)
	f := &fakeWaker{live: true}
	stubWaker(t, f)
	db := openFlowDB(t)
	*reg = "[" + bgAgentLive(bgFullSID, 4242, "idle", "done") + "]"
	t.Setenv(wakeOffEnv, "1") // keep the insert itself from pushing
	if err := flowdb.InsertBusMessage(db, &flowdb.BusMessage{
		ID: "msgnf001", CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: "user", ToAssignee: "user", ToTaskSlug: "bgt", Body: "queued",
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wakeOffEnv, "")
	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if c.resume != 0 || len(f.pushes) != 1 || !strings.Contains(f.pushes[0].Notice, "flow inbox pop") {
		t.Fatalf("resume=%d pushes=%+v, want one inbox notice", c.resume, f.pushes)
	}
}

// Alive, provably idle (nothing pending or in flight per its job state)
// and no push channel: stop it and resume the SAME id (no flags), which
// delivers the kickoff without a copy.
func TestCmdDoBackgroundIdleAliveNoPushStopsAndResumesSameID(t *testing.T) {
	c, reg := spawnBound(t)
	setConfirmTimeout(t, 0)
	stubWaker(t, &fakeWaker{live: false})
	*reg = "[" + bgAgentLive(bgFullSID, 4242, "idle", "done") + "]"
	writeJobState(t, bgFullSID[:8], jobStateQuiet)
	c.onStop = func() { *reg = bgAgentsJSONExited(bgFullSID, "stopped") }
	c.onResume = func() { *reg = "[" + bgAgentLive(bgFullSID, 4343, "busy", "working") + "]" }
	if rc := cmdDo([]string{"--with", "rebase on main", "bgt"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if c.stop != 1 || c.resume != 1 {
		t.Fatalf("stop=%d resume=%d, want stop then same-id resume", c.stop, c.resume)
	}
	noFlags(t, c.lastResume)
	if !strings.Contains(resumePrompt(c.lastResume), "rebase on main") {
		t.Errorf("resume prompt lost --with: %v", c.lastResume)
	}
	task, _ := flowdb.GetTask(openFlowDB(t), "bgt")
	if task.SessionID.String != bgFullSID {
		t.Errorf("session_id = %q, want %q", task.SessionID.String, bgFullSID)
	}
}

// Alive and idle but nothing to deliver: leave it alone, just report.
func TestCmdDoBackgroundIdleAliveNothingToDeliverReports(t *testing.T) {
	c, reg := spawnBound(t)
	f := &fakeWaker{live: true}
	stubWaker(t, f)
	*reg = "[" + bgAgentLive(bgFullSID, 4242, "idle", "done") + "]"
	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if c.resume != 0 || c.stop != 0 || len(f.pushes) != 0 {
		t.Fatalf("resume=%d stop=%d pushes=%d, want a plain report", c.resume, c.stop, len(f.pushes))
	}
}

// Busy: never interrupt and never copy. A --with that can't be delivered
// now is called out, not silently dropped.
func TestCmdDoBackgroundBusyWithIsNotDropped(t *testing.T) {
	c, reg := spawnBound(t)
	*reg = "[" + bgAgentLive(bgFullSID, 4242, "busy", "working") + "]"
	errOut := captureStderr(t)
	rc := cmdDo([]string{"--with", "rebase on main", "bgt"})
	stderr := errOut()
	if rc != 0 || c.resume != 0 || c.stop != 0 {
		t.Fatalf("rc=%d resume=%d stop=%d, want a report only", rc, c.resume, c.stop)
	}
	if !strings.Contains(stderr, "flow message") {
		t.Errorf("stderr should say --with was not delivered and point at flow message:\n%s", stderr)
	}
}

// Last resort: claude still started a copy (e.g. the session came alive
// between flow's check and the resume). flow binds the copy, records its
// lineage, and stops the original so two sessions don't share the task.
func TestCmdDoBackgroundForkFallbackStopsOriginal(t *testing.T) {
	c, reg := spawnBound(t)
	setConfirmTimeout(t, 0)
	*reg = bgAgentsJSONExited(bgFullSID, "stopped")
	c.resumeBanner = "note: session 48d287d9 is already running in the background, so this started a copy as 77665544.\n" +
		"backgrounded · 77665544\n"
	c.onResume = func() {
		*reg = "[" + bgAgentLive(bgFullSID, 4242, "idle", "done") + "," +
			bgAgentLive(resumeSID, 4343, "busy", "working") + "]"
	}
	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "bgt")
	if task.SessionID.String != resumeSID {
		t.Fatalf("session_id = %q, want the copy %q", task.SessionID.String, resumeSID)
	}
	if src, ok := flowdb.SessionSource(db, resumeSID); !ok || src != bgFullSID {
		t.Errorf("lineage = %q,%v, want %q", src, ok, bgFullSID)
	}
	if c.stop != 1 || !containsArg(c.lastStop, bgFullSID[:8]) {
		t.Errorf("stop=%d argv=%v, want the original %s stopped", c.stop, c.lastStop, bgFullSID[:8])
	}
}

// `claude stop` returns before the process is gone (verified live: an
// immediate `--bg --resume` still says "already running" and starts a
// copy). flow must wait until the registry shows the session exited
// before resuming it.
func TestCmdDoBackgroundStopWaitsForExitBeforeResume(t *testing.T) {
	c, reg := spawnBound(t)
	setConfirmTimeout(t, 0)
	setStopWait(t)
	stubWaker(t, &fakeWaker{live: false})
	live := "[" + bgAgentLive(bgFullSID, 4242, "idle", "done") + "]"
	*reg = live
	writeJobState(t, bgFullSID[:8], jobStateQuiet)
	stopped := false
	polls := 0
	c.onStop = func() { stopped = true }
	old := claude.BGCommandRunner
	claude.BGCommandRunner = func(workDir string, args []string) ([]byte, error) {
		if stopped && len(args) > 0 && args[0] == "agents" && *reg == live {
			polls++
			if polls >= 3 { // still alive for two polls after stop returned
				*reg = bgAgentsJSONExited(bgFullSID, "stopped")
			}
		}
		return old(workDir, args)
	}
	c.onResume = func() {
		if *reg == live {
			t.Errorf("resume issued while the stopped session was still alive")
		}
		*reg = "[" + bgAgentLive(bgFullSID, 4343, "busy", "working") + "]"
	}
	if rc := cmdDo([]string{"--with", "x", "bgt"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if c.stop != 1 || c.resume != 1 {
		t.Fatalf("stop=%d resume=%d", c.stop, c.resume)
	}
}

func setStopWait(t *testing.T) {
	t.Helper()
	oldT, oldP := bgStopWaitTimeout, bgStopWaitPoll
	bgStopWaitTimeout, bgStopWaitPoll = 2*time.Second, time.Millisecond
	t.Cleanup(func() { bgStopWaitTimeout, bgStopWaitPoll = oldT, oldP })
}
