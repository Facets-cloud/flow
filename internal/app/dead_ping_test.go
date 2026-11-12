package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness"
)

type deadPingRow struct {
	toA, toS, from, body string
	fyi, ping            bool
}

func deadPingRows(t *testing.T) []deadPingRow {
	t.Helper()
	db := openFlowDB(t)
	rows, err := db.Query(`SELECT to_assignee, COALESCE(to_task_slug,''), COALESCE(from_task_slug,''),
		body, fyi, ping FROM bus_messages ORDER BY created_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []deadPingRow
	for rows.Next() {
		var r deadPingRow
		if err := rows.Scan(&r.toA, &r.toS, &r.from, &r.body, &r.fyi, &r.ping); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// seedDeadPingTask creates a running auto task with a recognizable brief
// first line and a recorded auto-run log path.
func seedDeadPingTask(t *testing.T, root, slug string) string {
	t.Helper()
	seedTask(t, slug)
	seedRunningAuto(t, slug, 4242)
	brief := filepath.Join(root, "tasks", slug, "brief.md")
	if err := os.WriteFile(brief, []byte("\n# Answer alice: how do you do db migrations?\n\nmore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "tasks", slug, "auto-runs", "2026-11-09-120000.log")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET auto_run_log=? WHERE slug=?`, logPath, slug); err != nil {
		t.Fatal(err)
	}
	return logPath
}

func failingRunner(t *testing.T) {
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		return errors.New("Not logged in")
	})
}

// A parentless auto run that ends dead pings the human exactly once, with
// the slug, the brief's first line and the log path.
func TestDeadParentlessAutoRunPingsHuman(t *testing.T) {
	root := setupFlowRoot(t)
	logPath := seedDeadPingTask(t, root, "orphan")
	failingRunner(t)

	if rc := cmdAutoExec([]string{"orphan"}); rc != 1 {
		t.Fatalf("rc=%d, want 1", rc)
	}
	rows := deadPingRows(t)
	if len(rows) != 1 {
		t.Fatalf("want exactly 1 message, got %d: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.toA != "user" || r.toS != "" {
		t.Errorf("to=%q/%q, want the human", r.toA, r.toS)
	}
	if r.fyi || !r.ping {
		t.Errorf("fyi=%v ping=%v, want a pinging (non-FYI) message", r.fyi, r.ping)
	}
	if r.from != "orphan" {
		t.Errorf("from_task_slug=%q, want orphan", r.from)
	}
	for _, want := range []string{"orphan", "died without finishing", "Answer alice: how do you do db migrations?", logPath, "flow do orphan"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("body missing %q: %s", want, r.body)
		}
	}
	if strings.Contains(r.body, "# Answer") {
		t.Errorf("brief heading marker should be stripped: %s", r.body)
	}

	// Re-finalizing (a stale reconcile on read) must not notify again.
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "orphan")
	reconcileAutoRun(db, task)
	if n := len(deadPingRows(t)); n != 1 {
		t.Errorf("re-finalize double-notified: %d messages", n)
	}
}

// A parented dead run keeps the existing behavior: only the parent notice.
func TestDeadParentedAutoRunOnlyNotifiesParent(t *testing.T) {
	root := setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "papa", "sid-papa")
	seedDeadPingTask(t, root, "kid")
	if _, err := db.Exec(`UPDATE tasks SET parent_slug='papa' WHERE slug='kid'`); err != nil {
		t.Fatal(err)
	}
	failingRunner(t)

	cmdAutoExec([]string{"kid"})
	rows := deadPingRows(t)
	if len(rows) != 1 || rows[0].toS != "papa" {
		t.Fatalf("want only the parent notice, got %+v", rows)
	}
}

// A completed parentless run sends nothing.
func TestCompletedParentlessAutoRunSendsNothing(t *testing.T) {
	root := setupFlowRoot(t)
	seedDeadPingTask(t, root, "orphan")
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		_, err := openFlowDB(t).Exec(`UPDATE tasks SET status='done' WHERE slug='orphan'`)
		return err
	})
	if rc := cmdAutoExec([]string{"orphan"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if rows := deadPingRows(t); len(rows) != 0 {
		t.Errorf("completed run sent %+v", rows)
	}
}

// An owner-managed task (tag owner:<slug>) is reviewed by its owner's
// next tick, so a dead run does not also ping the human.
func TestDeadOwnerManagedAutoRunDoesNotPing(t *testing.T) {
	root := setupFlowRoot(t)
	seedDeadPingTask(t, root, "chore")
	db := openFlowDB(t)
	if err := flowdb.AddTaskTag(db, "chore", "owner:repo-keeper"); err != nil {
		t.Fatal(err)
	}
	failingRunner(t)
	cmdAutoExec([]string{"chore"})
	if rows := deadPingRows(t); len(rows) != 0 {
		t.Errorf("owner-managed dead run pinged: %+v", rows)
	}
}

// A run whose supervisor itself died (kill -9, reboot) is settled dead on
// read; a parentless one pings the human once — and only once.
func TestReconciledDeadParentlessAutoRunPingsOnce(t *testing.T) {
	root := setupFlowRoot(t)
	seedDeadPingTask(t, root, "orphan")
	oldAlive := processAlive
	processAlive = func(pid int) bool { return false }
	t.Cleanup(func() { processAlive = oldAlive })

	db := openFlowDB(t)
	for i := 0; i < 2; i++ {
		task, _ := flowdb.GetTask(db, "orphan")
		reconcileAutoRun(db, task)
	}
	rows := deadPingRows(t)
	if len(rows) != 1 || rows[0].toA != "user" || rows[0].fyi {
		t.Fatalf("want one pinging human message, got %+v", rows)
	}
}

// A run that reconciles to completed (task already done) sends nothing.
func TestReconciledCompletedRunSendsNothing(t *testing.T) {
	root := setupFlowRoot(t)
	seedDeadPingTask(t, root, "orphan")
	oldAlive := processAlive
	processAlive = func(pid int) bool { return false }
	t.Cleanup(func() { processAlive = oldAlive })
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET status='done' WHERE slug='orphan'`); err != nil {
		t.Fatal(err)
	}
	task, _ := flowdb.GetTask(db, "orphan")
	reconcileAutoRun(db, task)
	if rows := deadPingRows(t); len(rows) != 0 {
		t.Errorf("completed reconcile sent %+v", rows)
	}
}
