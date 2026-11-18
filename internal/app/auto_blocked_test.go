package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/listfmt"
)

// finalAutoRun returns the task's auto-run status after a supervisor ran.
func finalAutoRun(t *testing.T, slug string) *flowdb.Task {
	t.Helper()
	task, err := flowdb.GetTask(openFlowDB(t), slug)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// A child that ends with a `BLOCKED:` line in its log (claude -p prints
// only the final message to stdout, so the log holds it) and exits non-zero
// finalizes as blocked, not dead, and exits 0: the run ended on purpose.
func TestAutoExecFinalizesBlockedFromLogLine(t *testing.T) {
	root := setupFlowRoot(t)
	logPath := seedDeadPingTask(t, root, "stuck")
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(logPath, []byte("flow: started\nBLOCKED: need the staging API token\n"), 0o644); err != nil {
			return err
		}
		return errors.New("exit status 1")
	})

	if rc := cmdAutoExec([]string{"stuck"}); rc != 0 {
		t.Fatalf("rc=%d, want 0 for a blocked run", rc)
	}
	task := finalAutoRun(t, "stuck")
	if task.AutoRunStatus.String != "blocked" {
		t.Errorf("auto_run_status = %q, want blocked", task.AutoRunStatus.String)
	}
	if !task.AutoRunFinished.Valid || task.AutoRunPID.Valid {
		t.Errorf("blocked must set auto_run_finished and clear the pid: finished=%v pid=%v",
			task.AutoRunFinished, task.AutoRunPID)
	}
	// A blocked child reports itself; flow must not send the dead-child
	// notice as if it crashed.
	if rows := deadPingRows(t); len(rows) != 0 {
		t.Errorf("blocked run sent %d notices, want none: %+v", len(rows), rows)
	}
}

// When the harness writes the final message to --last-message (codex -o),
// that file is the authority: a BLOCKED line in the log alone is ignored.
func TestAutoExecBlockedFromLastMessageFile(t *testing.T) {
	root := setupFlowRoot(t)
	seedDeadPingTask(t, root, "stuck")
	lastMsg := filepath.Join(root, "last.txt")
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		return os.WriteFile(lastMsg, []byte("did part A\n\nBLOCKED: waiting on the DB migration owner\n"), 0o644)
	})

	if rc := cmdAutoExec([]string{"stuck", "--last-message", lastMsg}); rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
	if got := finalAutoRun(t, "stuck").AutoRunStatus.String; got != "blocked" {
		t.Errorf("auto_run_status = %q, want blocked", got)
	}
}

func TestAutoExecLastMessageFileIsAuthoritative(t *testing.T) {
	root := setupFlowRoot(t)
	logPath := seedDeadPingTask(t, root, "crashy")
	lastMsg := filepath.Join(root, "last.txt")
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		// The final message has no BLOCKED line; the log's tool chatter does.
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(logPath, []byte("BLOCKED: quoted from a tool result\n"), 0o644); err != nil {
			return err
		}
		return os.WriteFile(lastMsg, []byte("crashed mid-way\n"), 0o644)
	})

	cmdAutoExec([]string{"crashy", "--last-message", lastMsg})
	if got := finalAutoRun(t, "crashy").AutoRunStatus.String; got != "dead" {
		t.Errorf("auto_run_status = %q, want dead (the last-message file has no BLOCKED line)", got)
	}
}

// Only a line that STARTS with BLOCKED: counts; prose that mentions it does not.
func TestAutoExecBlockedMustStartALine(t *testing.T) {
	root := setupFlowRoot(t)
	logPath := seedDeadPingTask(t, root, "prose")
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(logPath, []byte("note: BLOCKED: not a status line\n"), 0o644)
	})

	cmdAutoExec([]string{"prose"})
	if got := finalAutoRun(t, "prose").AutoRunStatus.String; got != "dead" {
		t.Errorf("auto_run_status = %q, want dead", got)
	}
}

// `flow update task --waiting` during the run is the other blocked signal.
func TestAutoExecBlockedFromWaitingOn(t *testing.T) {
	root := setupFlowRoot(t)
	seedDeadPingTask(t, root, "waits")
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		_, err := openFlowDB(t).Exec(`UPDATE tasks SET waiting_on='design sign-off from alice' WHERE slug='waits'`)
		return err
	})

	if rc := cmdAutoExec([]string{"waits"}); rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
	if got := finalAutoRun(t, "waits").AutoRunStatus.String; got != "blocked" {
		t.Errorf("auto_run_status = %q, want blocked", got)
	}
}

// A waiting_on that was already set before the run is not this run's block.
func TestAutoExecPreexistingWaitingOnIsNotBlocked(t *testing.T) {
	root := setupFlowRoot(t)
	seedDeadPingTask(t, root, "old-wait")
	if _, err := openFlowDB(t).Exec(`UPDATE tasks SET waiting_on='set last week' WHERE slug='old-wait'`); err != nil {
		t.Fatal(err)
	}
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		return errors.New("harness exploded")
	})

	cmdAutoExec([]string{"old-wait"})
	if got := finalAutoRun(t, "old-wait").AutoRunStatus.String; got != "dead" {
		t.Errorf("auto_run_status = %q, want dead", got)
	}
}

// A self-`flow done` wins over any BLOCKED line: the work is finished.
func TestAutoExecDoneWinsOverBlocked(t *testing.T) {
	root := setupFlowRoot(t)
	logPath := seedDeadPingTask(t, root, "finished")
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(logPath, []byte("BLOCKED: stale note from earlier\n"), 0o644); err != nil {
			return err
		}
		_, err := openFlowDB(t).Exec(`UPDATE tasks SET status='done' WHERE slug='finished'`)
		return err
	})

	cmdAutoExec([]string{"finished"})
	if got := finalAutoRun(t, "finished").AutoRunStatus.String; got != "completed" {
		t.Errorf("auto_run_status = %q, want completed", got)
	}
}

func TestBlockedRunColorIsYellow(t *testing.T) {
	if got := autoRunColor("blocked"); got != listfmt.Yellow {
		t.Errorf("autoRunColor(blocked) = %q, want yellow", got)
	}
}

// The bootstrap prompt must tell a stuck child how to say so, or the
// BLOCKED: signal is unreachable.
func TestBootstrapPromptAsksForBlockedLine(t *testing.T) {
	p := buildAutoBootstrapPrompt("s", "regular", "", "")
	if !strings.Contains(p, "line starting `BLOCKED:`") {
		t.Errorf("prompt does not instruct the BLOCKED: line:\n%s", p)
	}
}

// Transient SQLite errors are retried with backoff; anything else is not.
func TestRetryTransientDBRetriesThenSucceeds(t *testing.T) {
	old := dbRetryBackoff
	dbRetryBackoff = 0
	t.Cleanup(func() { dbRetryBackoff = old })

	calls := 0
	err := retryTransientDB(func() error {
		calls++
		if calls < 3 {
			return errors.New("unable to open database file (14)")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Errorf("err=%v calls=%d, want nil after 3 calls", err, calls)
	}
}

func TestRetryTransientDBBusyAndGiveUp(t *testing.T) {
	old := dbRetryBackoff
	dbRetryBackoff = 0
	t.Cleanup(func() { dbRetryBackoff = old })

	calls := 0
	err := retryTransientDB(func() error {
		calls++
		return errors.New("database is locked (SQLITE_BUSY)")
	})
	if err == nil || calls != dbRetryAttempts {
		t.Errorf("err=%v calls=%d, want an error after %d attempts", err, calls, dbRetryAttempts)
	}
}

func TestRetryTransientDBDoesNotRetryOtherErrors(t *testing.T) {
	calls := 0
	err := retryTransientDB(func() error {
		calls++
		return errors.New("no such column: nope")
	})
	if err == nil || calls != 1 {
		t.Errorf("err=%v calls=%d, want one call and the error", err, calls)
	}
}
