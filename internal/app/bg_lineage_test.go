package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness/claude"
)

const (
	forkSID   = "aaaa1111-0000-4000-8000-000000000000"
	resumeSID = "77665544-0000-4000-8000-000000000000"
)

// bgAgentLive is one live registry entry with an explicit status/state.
func bgAgentLive(sid string, pid int, status, state string) string {
	return fmt.Sprintf(
		`{"pid":%d,"id":%q,"cwd":"/w","kind":"background","startedAt":1,"sessionId":%q,"name":"n","status":%q,"state":%q}`,
		pid, sid[:8], sid, status, state)
}

func setConfirmTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	oldT, oldP := bgResumeConfirmTimeout, bgResumeConfirmPoll
	bgResumeConfirmTimeout, bgResumeConfirmPoll = d, time.Millisecond
	t.Cleanup(func() { bgResumeConfirmTimeout, bgResumeConfirmPoll = oldT, oldP })
}

// spawnBound runs a first bg cmdDo that binds bgFullSID to task bgt and
// gives that session a transcript.
func spawnBound(t *testing.T) (*bgCalls, *string) {
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
	return c, &reg
}

// bindFork re-binds bgt to forkSID — a fork of bgFullSID that never took a
// turn (no transcript) — the state a prompt-less or failed bg resume left.
func bindFork(t *testing.T, recordLineage bool) {
	t.Helper()
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET session_id=? WHERE slug='bgt'`, forkSID); err != nil {
		t.Fatal(err)
	}
	if recordLineage {
		if err := flowdb.RecordSessionSource(db, forkSID, bgFullSID, "bgt"); err != nil {
			t.Fatal(err)
		}
	}
}

func writeJobDetail(t *testing.T, short, state, detail string) {
	t.Helper()
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".claude", "jobs", short)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"state":%q,"detail":%q}`, state, detail)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Phantom id: the resume's daemon job failed seconds after the banner
// ("source session … not found"). flow must NOT record the failed job's id
// — it keeps the prior binding and exits non-zero with the job's detail.
func TestCmdDoBackgroundResumeFailedJobKeepsPriorID(t *testing.T) {
	c, reg := spawnBound(t)
	setConfirmTimeout(t, 2*time.Second)
	*reg = bgAgentsJSONExited(bgFullSID, "stopped")
	c.resumeBanner = "backgrounded · 77665544 · bgt\n"
	c.onResume = func() {
		*reg = bgAgentsJSONExited(resumeSID, "failed")
		writeJobDetail(t, "77665544", "failed", "source session 48d287d9 not found")
	}
	errOut := captureStderr(t)
	rc := cmdDo([]string{"bgt"})
	stderr := errOut()
	if rc == 0 {
		t.Fatal("rc=0 for a resume whose job failed")
	}
	if !strings.Contains(stderr, "source session 48d287d9 not found") {
		t.Errorf("stderr lacks the job's failure detail:\n%s", stderr)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "bgt")
	if task.SessionID.String != bgFullSID {
		t.Errorf("session_id = %q, want prior %q kept", task.SessionID.String, bgFullSID)
	}
	if _, ok, _ := flowdb.SessionSource(db, resumeSID); ok {
		t.Error("lineage recorded for a failed resume")
	}
}

// A successful resume records the new id AND its fork source.
func TestCmdDoBackgroundResumeRecordsLineage(t *testing.T) {
	c, reg := spawnBound(t)
	setConfirmTimeout(t, 2*time.Second)
	*reg = bgAgentsJSONExited(bgFullSID, "stopped")
	c.resumeBanner = "backgrounded · 77665544 · bgt\n"
	c.onResume = func() {
		*reg = bgAgentsJSON(resumeSID)
		task, _ := flowdb.GetTask(openFlowDB(t), "bgt")
		writeClaudeTranscript(t, task.WorkDir, resumeSID) // took its kickoff turn
	}
	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "bgt")
	if task.SessionID.String != resumeSID {
		t.Fatalf("session_id = %q, want %q", task.SessionID.String, resumeSID)
	}
	if src, ok, _ := flowdb.SessionSource(db, resumeSID); !ok || src != bgFullSID {
		t.Errorf("lineage source = %q ok=%v, want %q", src, ok, bgFullSID)
	}
}

// History loss: the bound id never took a turn (no transcript) but its
// fork source has one. Resume from the source — a fresh spawn would throw
// the conversation away.
func TestCmdDoBackgroundTranscriptlessResumesFromAncestor(t *testing.T) {
	c, reg := spawnBound(t)
	bindFork(t, true)
	*reg = bgAgentsJSONExited(forkSID, "stopped")
	c.resumeBanner = "backgrounded · 77665544 · bgt\n"
	c.onResume = func() { *reg = bgAgentsJSON(resumeSID) }
	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if c.resume != 1 || c.spawn != 1 {
		t.Fatalf("resume=%d spawn=%d, want one resume and no new spawn", c.resume, c.spawn)
	}
	if !pairArg(c.lastResume, "--resume", bgFullSID) {
		t.Errorf("resumed %v, want --resume %s (the transcript-bearing source)", c.lastResume, bgFullSID)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "bgt")
	if task.SessionID.String != resumeSID {
		t.Errorf("session_id = %q, want %q", task.SessionID.String, resumeSID)
	}
	if src, _, _ := flowdb.SessionSource(db, resumeSID); src != bgFullSID {
		t.Errorf("lineage source = %q, want %q", src, bgFullSID)
	}
}

// A LIVE fork that is idle and never took a turn holds no conversation and
// has no wake channel; flow stops it and resumes from its source instead
// of reporting it "running" forever.
func TestCmdDoBackgroundStopsIdleNeverTurnedFork(t *testing.T) {
	c, reg := spawnBound(t)
	bindFork(t, true)
	*reg = "[" + bgAgentLive(forkSID, 4242, "idle", "blocked") + "]"
	c.onStop = func() { *reg = bgAgentsJSONExited(forkSID, "stopped") }
	c.resumeBanner = "backgrounded · 77665544 · bgt\n"
	c.onResume = func() { *reg = bgAgentsJSON(resumeSID) }
	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if c.stop != 1 || !containsArg(c.lastStop, forkSID[:8]) {
		t.Fatalf("stop=%d argv=%v, want claude stop %s", c.stop, c.lastStop, forkSID[:8])
	}
	if c.resume != 1 || !pairArg(c.lastResume, "--resume", bgFullSID) {
		t.Fatalf("resume=%d argv=%v, want --resume %s", c.resume, c.lastResume, bgFullSID)
	}
}

// Forks left by older binaries have no lineage row; the live process's
// command line (`--fork-session --resume <source>`) names the source.
func TestCmdDoBackgroundLegacyForkSourceFromProcess(t *testing.T) {
	c, reg := spawnBound(t)
	bindFork(t, false)
	*reg = "[" + bgAgentLive(forkSID, 4242, "idle", "blocked") + "]"
	oldPS := claude.PSRunner
	claude.PSRunner = func() ([]byte, error) {
		return []byte("  PID COMMAND\n 4242 /Users/x/.local/share/claude/versions/2.1.325 --session-id " +
			forkSID + " --fork-session --resume " + bgFullSID + " --name flow/bgt --dangerously-skip-permissions\n"), nil
	}
	t.Cleanup(func() { claude.PSRunner = oldPS })
	c.onStop = func() { *reg = bgAgentsJSONExited(forkSID, "stopped") }
	c.resumeBanner = "backgrounded · 77665544 · bgt\n"
	c.onResume = func() { *reg = bgAgentsJSON(resumeSID) }
	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if c.resume != 1 || !pairArg(c.lastResume, "--resume", bgFullSID) {
		t.Fatalf("resume=%d argv=%v, want --resume %s", c.resume, c.lastResume, bgFullSID)
	}
}

// A live transcript-less session that is BUSY may be mid first turn (its
// transcript not flushed yet): leave it alone.
func TestCmdDoBackgroundBusyTranscriptlessIsLeftRunning(t *testing.T) {
	c, reg := spawnBound(t)
	bindFork(t, true)
	*reg = "[" + bgAgentLive(forkSID, 4242, "busy", "working") + "]"
	if rc := cmdDo([]string{"bgt"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if c.stop != 0 || c.resume != 0 || c.spawn != 1 {
		t.Fatalf("stop=%d resume=%d spawn=%d, want a no-op report", c.stop, c.resume, c.spawn)
	}
}

// An idle never-turned fork whose history source is unknown (no lineage,
// process command line doesn't name one — e.g. a claimed bg-spare) is NOT
// stopped: stopping it would only lead to a fresh spawn that drops the
// conversation. flow reports it and points at --fresh.
func TestCmdDoBackgroundIdleForkWithUnknownSourceIsNotStopped(t *testing.T) {
	c, reg := spawnBound(t)
	bindFork(t, false)
	*reg = "[" + bgAgentLive(forkSID, 4242, "idle", "blocked") + "]"
	oldPS := claude.PSRunner
	claude.PSRunner = func() ([]byte, error) {
		return []byte("  PID COMMAND\n 4242 claude bg-spare --bg-spare /tmp/cc-daemon/spare/x.claim.sock\n"), nil
	}
	t.Cleanup(func() { claude.PSRunner = oldPS })
	out := captureStdout(t, func() {
		if rc := cmdDo([]string{"bgt"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if c.stop != 0 || c.resume != 0 || c.spawn != 1 {
		t.Fatalf("stop=%d resume=%d spawn=%d, want no stop/resume/spawn", c.stop, c.resume, c.spawn)
	}
	if !strings.Contains(out, "--fresh") {
		t.Errorf("output lacks the --fresh hint:\n%s", out)
	}
}
