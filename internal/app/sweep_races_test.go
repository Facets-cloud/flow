package app

import (
	"errors"
	"flow/internal/flowdb"
	"flow/internal/harness"
	"strings"
	"testing"
)

// Races around the deferred Codex close-out sweep. For Codex auto runs the
// supervisor sweeps AFTER the harness exits and BEFORE finalizeAutoRun, so
// there is a window W where status=done, auto_run=running and the
// supervisor pid is alive.

func stubAlive(t *testing.T, fn func(pid int) bool) {
	t.Helper()
	old := processAlive
	processAlive = fn
	t.Cleanup(func() { processAlive = old })
}

func setTaskDone(t *testing.T, slug string) {
	t.Helper()
	db := openFlowDB(t)
	defer db.Close()
	if _, err := db.Exec(`UPDATE tasks SET status='done' WHERE slug=?`, slug); err != nil {
		t.Fatal(err)
	}
}

// --- Race 1: the supervisor dies during W ---------------------------------

// A done task whose supervisor died mid-sweep finished its work: reconcile
// must record 'completed', not 'dead'.
func TestReconcileDoneTaskWithDeadPidIsCompleted(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "auto-task")
	seedRunningAuto(t, "auto-task", 999999)
	setTaskDone(t, "auto-task")
	stubAlive(t, func(int) bool { return false })

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "auto-task")
	if err != nil {
		t.Fatal(err)
	}
	reconcileAutoRun(db, task)
	if got := task.AutoRunStatus.String; got != "completed" {
		t.Errorf("in-memory auto_run=%q, want completed", got)
	}
	if task.AutoRunPID.Valid {
		t.Error("pid should be cleared")
	}
	if got := taskStatus(t, "auto-task").AutoRunStatus.String; got != "completed" {
		t.Errorf("persisted auto_run=%q, want completed", got)
	}
}

// The same holds when the stale row is reconciled through a read command.
func TestShowReconcilesDoneDeadSupervisorAsCompleted(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "auto-task")
	seedRunningAuto(t, "auto-task", 999999)
	setTaskDone(t, "auto-task")
	stubAlive(t, func(int) bool { return false })

	out := captureStdout(t, func() { cmdShow([]string{"task", "auto-task"}) })
	if strings.Contains(out, "dead") {
		t.Errorf("a done task must never show a dead auto run:\n%s", out)
	}
	if got := taskStatus(t, "auto-task").AutoRunStatus.String; got != "completed" {
		t.Errorf("auto_run=%q, want completed", got)
	}
}

// A child that marked itself done but whose harness exited non-zero
// (e.g. the run was cut during the sweep) is not a dead child: it gets
// 'completed' and its parent hears no crash notice.
func TestAutoExecDoneChildWithRunErrIsNotReportedDead(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "parent")
	seedTask(t, "child")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET parent_slug='parent' WHERE slug='child'`); err != nil {
		t.Fatal(err)
	}
	seedRunningAuto(t, "child", 4242)
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		setTaskDone(t, "child")
		return errors.New("harness exited 1 after done")
	})
	cmdAutoExec([]string{"child"})

	if got := taskStatus(t, "child").AutoRunStatus.String; got != "completed" {
		t.Errorf("auto_run=%q, want completed for a done child", got)
	}
	msgs, _ := pendingForIdentity(openFlowDB(t), busSender{Assignee: busUser, TaskSlug: "parent"})
	if len(msgs) != 0 {
		t.Errorf("a done child must not be reported dead; parent got %d messages", len(msgs))
	}
}

// --- Race 2: double / concurrent sweep ------------------------------------

// `flow done` on an already-done task is idempotent (rc 0) and does not
// run a second sweep or bump status_changed_at.
func TestDoneAlreadyDoneSkipsSweep(t *testing.T) {
	setupFlowRoot(t)
	seedHarnessTask(t, "cx", "codex")
	calls := stubCodexSweep(t, nil)
	t.Setenv(autoRunEnvVar, "")

	if rc := cmdDone([]string{"cx"}); rc != 0 {
		t.Fatalf("first done rc=%d", rc)
	}
	changed := taskStatus(t, "cx").StatusChangedAt
	out := captureStdout(t, func() {
		if rc := cmdDone([]string{"cx"}); rc != 0 {
			t.Fatalf("second done rc=%d, want 0 (idempotent)", rc)
		}
	})
	if len(*calls) != 1 {
		t.Errorf("sweep calls=%d, want 1 (second done must skip)", len(*calls))
	}
	if !strings.Contains(out, "already done") {
		t.Errorf("second done should say why it skipped; got %q", out)
	}
	if got := taskStatus(t, "cx").StatusChangedAt; got != changed {
		t.Errorf("status_changed_at moved on a no-op done: %v -> %v", changed, got)
	}
}

// A human `flow done` while the task's Codex auto run is alive leaves the
// sweep to the supervisor (the one sweeper).
func TestDoneDuringLiveCodexAutoRunDefersToSupervisor(t *testing.T) {
	setupFlowRoot(t)
	seedHarnessTask(t, "cx", "codex")
	seedRunningAuto(t, "cx", 4242)
	stubAlive(t, func(pid int) bool { return pid == 4242 })
	calls := stubCodexSweep(t, nil)
	t.Setenv(autoRunEnvVar, "") // the human's shell, not the run

	out := captureStdout(t, func() {
		if rc := cmdDone([]string{"cx"}); rc != 0 {
			t.Fatalf("done rc=%d", rc)
		}
	})
	if len(*calls) != 0 {
		t.Errorf("inline sweep calls=%d, want 0 (supervisor sweeps)", len(*calls))
	}
	if !strings.Contains(out, "deferred") {
		t.Errorf("should say the sweep is deferred; got %q", out)
	}
	if taskStatus(t, "cx").Status != "done" {
		t.Error("status flip must still happen")
	}
}

// If the Codex supervisor is gone, nobody else will sweep: sweep inline.
func TestDoneCodexDeadSupervisorSweepsInline(t *testing.T) {
	setupFlowRoot(t)
	seedHarnessTask(t, "cx", "codex")
	seedRunningAuto(t, "cx", 4242)
	stubAlive(t, func(int) bool { return false })
	calls := stubCodexSweep(t, nil)
	t.Setenv(autoRunEnvVar, "")
	if rc := cmdDone([]string{"cx"}); rc != 0 {
		t.Fatalf("done rc=%d", rc)
	}
	if len(*calls) != 1 {
		t.Errorf("sweep calls=%d, want 1", len(*calls))
	}
}

// Claude never defers, so a live Claude auto run doesn't suppress the
// human's inline sweep (the Claude supervisor doesn't sweep).
func TestDoneDuringLiveClaudeAutoRunSweepsInline(t *testing.T) {
	setupFlowRoot(t)
	seedHarnessTask(t, "cl", "claude")
	seedRunningAuto(t, "cl", 4242)
	stubAlive(t, func(int) bool { return true })
	calls := stubClaudeRunner(t, nil)
	t.Setenv(autoRunEnvVar, "")
	if rc := cmdDone([]string{"cl"}); rc != 0 {
		t.Fatalf("done rc=%d", rc)
	}
	if len(*calls) != 1 {
		t.Errorf("sweep calls=%d, want 1", len(*calls))
	}
}

// End to end: a human closes the task mid-run, then the run closes it
// itself, then the supervisor finishes. Exactly one sweep, by the
// supervisor, after the harness exited.
func TestAutoExecHumanDoneMidRunSweepsOnce(t *testing.T) {
	setupFlowRoot(t)
	seedHarnessTask(t, "cx", "codex")
	seedRunningAuto(t, "cx", 4242)
	stubAlive(t, func(pid int) bool { return pid == 4242 })
	harnessExited := false
	var sweptAfterExit []bool
	calls := stubCodexSweep(t, func() { sweptAfterExit = append(sweptAfterExit, harnessExited) })
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		t.Setenv(autoRunEnvVar, "") // human
		if rc := cmdDone([]string{"cx"}); rc != 0 {
			t.Fatalf("human done rc=%d", rc)
		}
		t.Setenv(autoRunEnvVar, "cx") // the run itself
		if rc := cmdDone([]string{"cx"}); rc != 0 {
			t.Fatalf("run done rc=%d", rc)
		}
		harnessExited = true
		return nil
	})
	captureStdout(t, func() {
		if rc := cmdAutoExec([]string{"cx"}); rc != 0 {
			t.Fatalf("cmdAutoExec rc=%d", rc)
		}
	})
	if len(*calls) != 1 {
		t.Fatalf("sweep calls=%d, want exactly 1", len(*calls))
	}
	if !sweptAfterExit[0] {
		t.Error("the one sweep must be the supervisor's, after the harness exited")
	}
	if got := taskStatus(t, "cx").AutoRunStatus.String; got != "completed" {
		t.Errorf("auto_run=%q, want completed", got)
	}
}

// --- Race 3: stale finalize overwrites a newer run ------------------------

// `flow do --auto --force` relaunched during W: the old supervisor's
// finalize must not stamp 'completed' over the new run or clear its pid.
func TestAutoExecStaleFinalizeLeavesNewerRunAlone(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "auto-task")
	seedRunningAuto(t, "auto-task", 4242)
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		setTaskDone(t, "auto-task")
		db := openFlowDB(t)
		defer db.Close()
		return recordAutoRunLaunched(db, "auto-task", 5555, "/runs/new.log")
	})
	out := captureStdout(t, func() { cmdAutoExec([]string{"auto-task"}) })

	got := taskStatus(t, "auto-task")
	if got.AutoRunStatus.String != "running" || got.AutoRunPID.Int64 != 5555 {
		t.Errorf("newer run clobbered: status=%q pid=%v", got.AutoRunStatus.String, got.AutoRunPID)
	}
	if got.AutoRunLog.String != "/runs/new.log" {
		t.Errorf("auto_run_log=%q, want the newer run's", got.AutoRunLog.String)
	}
	if !strings.Contains(out, "newer") {
		t.Errorf("supervisor should log that a newer run took over; got %q", out)
	}
}

// A superseded supervisor whose run ended not-done must not report the
// child dead to its parent: the newer run owns the outcome now.
func TestAutoExecSupersededRunSendsNoDeadNotice(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "parent")
	seedTask(t, "child")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET parent_slug='parent' WHERE slug='child'`); err != nil {
		t.Fatal(err)
	}
	seedRunningAuto(t, "child", 4242)
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		db := openFlowDB(t)
		defer db.Close()
		return recordAutoRunLaunched(db, "child", 5555, "/runs/new.log")
	})
	captureStdout(t, func() { cmdAutoExec([]string{"child"}) })

	got := taskStatus(t, "child")
	if got.AutoRunStatus.String != "running" || got.AutoRunPID.Int64 != 5555 {
		t.Errorf("newer run clobbered: status=%q pid=%v", got.AutoRunStatus.String, got.AutoRunPID)
	}
	msgs, _ := pendingForIdentity(openFlowDB(t), busSender{Assignee: busUser, TaskSlug: "parent"})
	if len(msgs) != 0 {
		t.Errorf("superseded run must not post a dead notice; parent got %d", len(msgs))
	}
}
