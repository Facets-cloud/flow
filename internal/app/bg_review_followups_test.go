package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness/claude"
)

// Follow-ups from the post-merge review of #170.

// writeJobState writes ~/.claude/jobs/<short>/state.json with the fields
// flow reads to decide whether a live idle session is provably idle.
func writeJobState(t *testing.T, short, body string) {
	t.Helper()
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".claude", "jobs", short)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const jobStateQuiet = `{"state":"working","tempo":"idle","needs":null,"needsAsk":null,` +
	`"inFlight":{"tasks":0,"queued":0,"kinds":[],"drainableMonitors":0}}`

// 1. A same-id resume always finds the id's old transcript, so the
// transcript check can't confirm it. A job that failed after its banner
// must be an error, and the binding stays as it was.
func TestCmdDoBackgroundSameIDResumeFailedJobIsError(t *testing.T) {
	c, reg := spawnBound(t)
	setConfirmTimeout(t, 2*time.Second)
	*reg = bgAgentsJSONExited(bgFullSID, "stopped")
	c.onResume = func() {
		*reg = bgAgentsJSONExited(bgFullSID, "failed")
		writeJobDetail(t, bgFullSID[:8], "failed", "model overloaded")
	}
	errOut := captureStderr(t)
	rc := cmdDo([]string{"bgt"})
	stderr := errOut()
	if rc == 0 {
		t.Fatal("rc=0 for a same-id resume whose job failed")
	}
	if !strings.Contains(stderr, "model overloaded") {
		t.Errorf("stderr lacks the job's failure detail:\n%s", stderr)
	}
	task, _ := flowdb.GetTask(openFlowDB(t), "bgt")
	if task.SessionID.String != bgFullSID {
		t.Errorf("session_id = %q, want %q unchanged", task.SessionID.String, bgFullSID)
	}
	if task.SessionLastResumed.Valid {
		t.Errorf("session_last_resumed = %q recorded for a failed resume", task.SessionLastResumed.String)
	}
}

// A same-id resume that comes up live is confirmed by its pid (its
// transcript already existed, so it proves nothing).
func TestCmdDoBackgroundSameIDResumeLiveIsConfirmed(t *testing.T) {
	c, reg := spawnBound(t)
	setConfirmTimeout(t, 2*time.Second)
	*reg = bgAgentsJSONExited(bgFullSID, "stopped")
	c.onResume = func() { *reg = "[" + bgAgentLive(bgFullSID, 4343, "busy", "working") + "]" }
	errOut := captureStderr(t)
	start := time.Now()
	rc := cmdDo([]string{"bgt"})
	stderr := errOut()
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, stderr)
	}
	if time.Since(start) > time.Second {
		t.Errorf("confirm waited %s for a session that was already live", time.Since(start))
	}
	if strings.Contains(stderr, "has not started") {
		t.Errorf("live same-id resume reported unconfirmed:\n%s", stderr)
	}
}

// 2. An idle session is only stopped (to deliver work) when it is
// provably idle. Waiting on the user (state blocked), background shell
// work or monitors in flight, or an unreadable job state all mean "not
// provably idle": leave it running and say the work is queued.
func TestCmdDoBackgroundIdleNotProvablyIdleIsNotStopped(t *testing.T) {
	cases := []struct {
		name, state, job string
	}{
		{"blocked on the user", "blocked", jobStateQuiet},
		{"needs an answer", "working", `{"state":"working","tempo":"idle","needsAsk":{"q":"?"},"inFlight":{"tasks":0,"queued":0,"drainableMonitors":0}}`},
		{"background shell in flight", "working", `{"state":"working","tempo":"idle","inFlight":{"tasks":1,"queued":0,"kinds":["local_bash"],"drainableMonitors":0}}`},
		{"monitor in flight", "working", `{"state":"working","tempo":"idle","inFlight":{"tasks":0,"queued":0,"kinds":[],"drainableMonitors":1}}`},
		{"job state unknown", "working", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, reg := spawnBound(t)
			setConfirmTimeout(t, 0)
			stubWaker(t, &fakeWaker{live: false})
			*reg = "[" + bgAgentLive(bgFullSID, 4242, "idle", tc.state) + "]"
			if tc.job != "" {
				writeJobState(t, bgFullSID[:8], tc.job)
			}
			errOut := captureStderr(t)
			rc := cmdDo([]string{"--with", "rebase on main", "bgt"})
			stderr := errOut()
			if rc != 0 {
				t.Fatalf("rc=%d", rc)
			}
			if c.stop != 0 || c.resume != 0 {
				t.Fatalf("stop=%d resume=%d, want the session left running", c.stop, c.resume)
			}
			if !strings.Contains(stderr, "NOT delivered") || !strings.Contains(stderr, "flow message") {
				t.Errorf("stderr should say --with was not delivered and how to send it:\n%s", stderr)
			}
		})
	}
}

// Queued mail for a not-provably-idle session stays queued, and flow says
// so instead of stopping the session.
func TestCmdDoBackgroundIdleBlockedMailStaysQueued(t *testing.T) {
	c, reg := spawnBound(t)
	stubWaker(t, &fakeWaker{live: false})
	db := openFlowDB(t)
	if err := flowdb.InsertBusMessage(db, &flowdb.BusMessage{
		ID: "msgrf001", CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: "user", ToAssignee: "user", ToTaskSlug: "bgt", Body: "queued",
	}); err != nil {
		t.Fatal(err)
	}
	*reg = "[" + bgAgentLive(bgFullSID, 4242, "idle", "blocked") + "]"
	writeJobState(t, bgFullSID[:8], jobStateQuiet)
	errOut := captureStderr(t)
	rc := cmdDo([]string{"bgt"})
	stderr := errOut()
	if rc != 0 || c.stop != 0 || c.resume != 0 {
		t.Fatalf("rc=%d stop=%d resume=%d, want a report only", rc, c.stop, c.resume)
	}
	if !strings.Contains(stderr, "queued") {
		t.Errorf("stderr should say the mail stays queued:\n%s", stderr)
	}
}

// 3. A registered resume passes no launch flags, so a requested
// --dangerously-skip-permissions can't be applied. Say so.
func TestCmdDoBackgroundRegisteredResumeNotesSkipPermsDropped(t *testing.T) {
	c, reg := spawnBound(t)
	setConfirmTimeout(t, 0)
	*reg = bgAgentsJSONExited(bgFullSID, "stopped")
	c.onResume = func() { *reg = "[" + bgAgentLive(bgFullSID, 4242, "busy", "working") + "]" }
	errOut := captureStderr(t)
	rc := cmdDo([]string{"--dangerously-skip-permissions", "bgt"})
	stderr := errOut()
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(stderr, "--dangerously-skip-permissions") || !strings.Contains(stderr, "saved permission mode") {
		t.Errorf("stderr should note the flag was not applied:\n%s", stderr)
	}
}

// No note when the flag wasn't asked for.
func TestCmdDoBackgroundRegisteredResumeNoSkipPermsNoNote(t *testing.T) {
	c, reg := spawnBound(t)
	setConfirmTimeout(t, 0)
	*reg = bgAgentsJSONExited(bgFullSID, "stopped")
	c.onResume = func() { *reg = "[" + bgAgentLive(bgFullSID, 4242, "busy", "working") + "]" }
	errOut := captureStderr(t)
	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if s := errOut(); strings.Contains(s, "saved permission mode") {
		t.Errorf("unexpected skip-permissions note:\n%s", s)
	}
}

// 4. A legacy fork's source, read from its process, is recorded as lineage
// BEFORE the fork is stopped: if the resume then fails, the next flow do
// still knows where the history lives.
func TestCmdDoBackgroundLegacyForkSourcePersistedBeforeStop(t *testing.T) {
	c, reg := spawnBound(t)
	setConfirmTimeout(t, 2*time.Second)
	bindFork(t, false)
	*reg = "[" + bgAgentLive(forkSID, 4242, "idle", "blocked") + "]"
	oldPS := claude.PSRunner
	claude.PSRunner = func() ([]byte, error) {
		return []byte("  PID COMMAND\n 4242 claude --session-id " + forkSID +
			" --fork-session --resume " + bgFullSID + " --name flow/bgt\n"), nil
	}
	t.Cleanup(func() { claude.PSRunner = oldPS })
	c.onStop = func() { *reg = bgAgentsJSONExited(forkSID, "stopped") }
	c.resumeBanner = "backgrounded · 77665544 · bgt\n"
	c.onResume = func() {
		*reg = bgAgentsJSONExited(resumeSID, "failed")
		writeJobDetail(t, "77665544", "failed", "boom")
	}
	if rc := cmdDo([]string{"bgt"}); rc == 0 {
		t.Fatal("rc=0 for a failed resume")
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "bgt")
	if task.SessionID.String != forkSID {
		t.Fatalf("session_id = %q, want %q kept", task.SessionID.String, forkSID)
	}
	if src, ok, _ := flowdb.SessionSource(db, forkSID); !ok || src != bgFullSID {
		t.Errorf("lineage for %s = %q,%v, want %q", forkSID, src, ok, bgFullSID)
	}
}

// 5. Resuming from an ancestor that claude keeps under the ancestor's own
// id is not "a copy".
func TestCmdDoBackgroundAncestorSameIDNotReportedAsCopy(t *testing.T) {
	c, reg := spawnBound(t)
	setConfirmTimeout(t, 0)
	bindFork(t, true)
	*reg = bgAgentsJSONExited(forkSID, "stopped")
	c.onResume = func() { *reg = "[" + bgAgentLive(bgFullSID, 4343, "busy", "working") + "]" }
	out := captureStdout(t, func() {
		if rc := cmdDo([]string{"bgt"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if strings.Contains(out, "copy") {
		t.Errorf("same-id ancestor resume reported as a copy:\n%s", out)
	}
	if !strings.Contains(out, bgFullSID) {
		t.Errorf("output lacks the resumed session id:\n%s", out)
	}
}

// The idle snapshot is re-read right before the stop: the delivery attempt
// in between can take a while, and a session that started a turn in that
// window must not be stopped.
func TestCmdDoBackgroundIdleRecheckedBeforeStop(t *testing.T) {
	c, reg := spawnBound(t)
	setConfirmTimeout(t, 0)
	stubWaker(t, &fakeWaker{live: false})
	*reg = "[" + bgAgentLive(bgFullSID, 4242, "idle", "working") + "]"
	writeJobState(t, bgFullSID[:8], jobStateQuiet)
	reads := 0
	old := claude.BGCommandRunner
	claude.BGCommandRunner = func(workDir string, args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "agents" {
			reads++
			if reads == 2 { // it started a turn after the first look
				*reg = "[" + bgAgentLive(bgFullSID, 4242, "busy", "working") + "]"
			}
		}
		return old(workDir, args)
	}
	errOut := captureStderr(t)
	rc := cmdDo([]string{"--with", "x", "bgt"})
	stderr := errOut()
	if rc != 0 || c.stop != 0 || c.resume != 0 {
		t.Fatalf("rc=%d stop=%d resume=%d, want the now-busy session left running", rc, c.stop, c.resume)
	}
	if !strings.Contains(stderr, "NOT delivered") {
		t.Errorf("stderr should say --with was not delivered:\n%s", stderr)
	}
}

// If the legacy fork's source can't be recorded, don't stop the process
// that is the only record of it.
func TestCmdDoBackgroundLegacyForkNotStoppedWhenLineageWriteFails(t *testing.T) {
	c, reg := spawnBound(t)
	bindFork(t, false)
	if _, err := openFlowDB(t).Exec(`CREATE TRIGGER lineage_boom BEFORE INSERT ON session_lineage BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	*reg = "[" + bgAgentLive(forkSID, 4242, "idle", "blocked") + "]"
	oldPS := claude.PSRunner
	claude.PSRunner = func() ([]byte, error) {
		return []byte("  PID COMMAND\n 4242 claude --session-id " + forkSID +
			" --fork-session --resume " + bgFullSID + " --name flow/bgt\n"), nil
	}
	t.Cleanup(func() { claude.PSRunner = oldPS })
	if rc := cmdDo([]string{"bgt"}); rc == 0 {
		t.Fatal("rc=0 though the fork source could not be recorded")
	}
	if c.stop != 0 || c.resume != 0 {
		t.Fatalf("stop=%d resume=%d, want the fork left running", c.stop, c.resume)
	}
}
