package app

import (
	"strings"
	"testing"

	"flow/internal/flowdb"
)

func seedStartedTask(t *testing.T, slug string) {
	t.Helper()
	seedTask(t, slug)
	db := openFlowDB(t)
	defer db.Close()
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=?, status='in-progress' WHERE slug=?`,
		fakeSessionID(slug), flowdb.NowISO(), slug,
	); err != nil {
		t.Fatal(err)
	}
}

// F5: a Codex driver closed itself with `flow update task --status done`,
// which flipped the status but skipped the close-out sweep — the KB /
// project distillation silently never happened. --status done now routes
// through the same path as `flow done`.
func TestUpdateStatusDoneRunsCloseoutSweep(t *testing.T) {
	setupFlowRoot(t)
	calls := stubClaudeRunner(t, nil)
	seedStartedTask(t, "closer")

	var rc int
	out := captureStdout(t, func() { rc = cmdUpdateTask([]string{"closer", "--status", "done", "--priority", "high"}) })
	if rc != 0 {
		t.Fatalf("rc=%d out=%s", rc, out)
	}
	if len(*calls) != 1 || !strings.Contains((*calls)[0].prompt, "closer") {
		t.Fatalf("close-out sweep calls=%d, want 1 for closer", len(*calls))
	}
	if !strings.Contains(out, "flow done") {
		t.Errorf("output does not say it routed through flow done: %q", out)
	}
	db := openFlowDB(t)
	defer db.Close()
	task, _ := flowdb.GetTask(db, "closer")
	if task.Status != "done" || task.Priority != "high" {
		t.Errorf("status=%q priority=%q, want done/high", task.Status, task.Priority)
	}
	// Re-running is idempotent: no second sweep.
	_ = captureStdout(t, func() { rc = cmdUpdateTask([]string{"closer", "--status", "done"}) })
	if rc != 0 || len(*calls) != 1 {
		t.Errorf("second --status done: rc=%d sweeps=%d, want 0/1", rc, len(*calls))
	}
}

// Scripted bulk uses keep a fast, sweep-free lane — explicitly.
func TestUpdateStatusDoneNoSweep(t *testing.T) {
	setupFlowRoot(t)
	calls := stubClaudeRunner(t, nil)
	seedStartedTask(t, "bulk")
	var rc int
	// captureStdout returns stdout+stderr combined.
	errOut := captureStdout(t, func() { rc = cmdUpdateTask([]string{"bulk", "--status", "done", "--no-sweep"}) })
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errOut)
	}
	if len(*calls) != 0 {
		t.Errorf("sweep ran under --no-sweep")
	}
	if !strings.Contains(errOut, "close-out sweep skipped") {
		t.Errorf("--no-sweep is silent: %q", errOut)
	}
	db := openFlowDB(t)
	defer db.Close()
	if task, _ := flowdb.GetTask(db, "bulk"); task.Status != "done" {
		t.Errorf("status=%q, want done", task.Status)
	}
}
