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

// Review note 2 on #132: on a task that never had a session, `--status
// done` used to write the other fields, print "status → done", and only
// then fail. The session check now runs before any field is written.
func TestUpdateStatusDoneSessionlessWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"sweep", []string{"fresh", "--priority", "high", "--tag", "zz", "--status", "done"}},
		{"no-sweep", []string{"fresh", "--priority", "high", "--tag", "zz", "--status", "done", "--no-sweep"}},
		{"in-progress", []string{"fresh", "--priority", "high", "--tag", "zz", "--status", "in-progress"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupFlowRoot(t)
			calls := stubClaudeRunner(t, nil)
			seedTask(t, "fresh")
			db := openFlowDB(t)
			defer db.Close()
			before, err := flowdb.GetTask(db, "fresh")
			if err != nil {
				t.Fatal(err)
			}

			var rc int
			out := captureStdout(t, func() { rc = cmdUpdateTask(tc.args) })
			if rc != 1 {
				t.Fatalf("rc=%d, want 1; out=%s", rc, out)
			}
			if strings.Contains(out, "status →") || strings.Contains(out, "priority →") || strings.Contains(out, "tag +") {
				t.Errorf("output claims a change that did not stick: %q", out)
			}
			if !strings.Contains(out, "session_id") {
				t.Errorf("error does not explain the missing session: %q", out)
			}
			after, err := flowdb.GetTask(db, "fresh")
			if err != nil {
				t.Fatal(err)
			}
			if after.Status != before.Status || after.Priority != before.Priority || after.UpdatedAt != before.UpdatedAt {
				t.Errorf("task changed: status %q→%q priority %q→%q updated_at %q→%q",
					before.Status, after.Status, before.Priority, after.Priority, before.UpdatedAt, after.UpdatedAt)
			}
			tags, err := flowdb.GetTaskTags(db, "fresh")
			if err != nil {
				t.Fatal(err)
			}
			if len(tags) != 0 {
				t.Errorf("tags written despite the failure: %v", tags)
			}
			if len(*calls) != 0 {
				t.Errorf("sweep ran %d time(s) for a task that never closed", len(*calls))
			}
		})
	}
}

// The "routed through flow done" line is printed once the status really
// flipped — never for a task that was already done.
func TestUpdateStatusDoneRoutedLineOnlyAfterFlip(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	seedStartedTask(t, "flipper")

	var rc int
	out := captureStdout(t, func() { rc = cmdUpdateTask([]string{"flipper", "--status", "done"}) })
	if rc != 0 {
		t.Fatalf("rc=%d out=%s", rc, out)
	}
	marked, routed := strings.Index(out, "Marked flipper as done"), strings.Index(out, "status → done")
	if marked < 0 || routed < 0 || routed < marked {
		t.Errorf("want the routed line after the flip, got: %q", out)
	}
	out = captureStdout(t, func() { rc = cmdUpdateTask([]string{"flipper", "--status", "done"}) })
	if rc != 0 || strings.Contains(out, "status → done") {
		t.Errorf("already-done task: rc=%d, must not claim a flip: %q", rc, out)
	}
}
