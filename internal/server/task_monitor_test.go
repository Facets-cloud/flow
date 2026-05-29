package server

import (
	"database/sql"
	"testing"
	"time"

	"flow/internal/flowdb"
)

func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func TestTaskNeedsMonitor(t *testing.T) {
	cases := []struct {
		name string
		task *flowdb.Task
		tags []string
		want bool
	}{
		{"slack-reply backlog", &flowdb.Task{Status: "backlog"}, []string{"slack-reply"}, true},
		{"gh-pr in-progress", &flowdb.Task{Status: "in-progress"}, []string{"gh-pr:o/r#1"}, true},
		{"gh-issue backlog", &flowdb.Task{Status: "backlog"}, []string{"gh-issue:o/r#9"}, true},
		{"slack-thread in-progress", &flowdb.Task{Status: "in-progress"}, []string{"slack-thread:C1:123.45"}, true},
		{"worktree but no origin tag", &flowdb.Task{Status: "backlog", WorktreePath: nullStr("/tmp/wt")}, nil, false},
		{"worktree + plain slack label only", &flowdb.Task{Status: "in-progress", WorktreePath: nullStr("/tmp/wt")}, []string{"bugfix", "cli", "slack"}, false},
		{"no origin no worktree", &flowdb.Task{Status: "backlog"}, []string{"ui", "p1"}, false},
		{"gh-pr but done", &flowdb.Task{Status: "done"}, []string{"gh-pr:o/r#1"}, false},
		{"gh-pr but archived", &flowdb.Task{Status: "backlog", ArchivedAt: nullStr("2026-05-01T00:00:00Z")}, []string{"gh-pr:o/r#1"}, false},
		{"gh-pr but deleted", &flowdb.Task{Status: "backlog", DeletedAt: nullStr("2026-05-01T00:00:00Z")}, []string{"gh-pr:o/r#1"}, false},
		{"nil task", nil, []string{"slack-reply"}, false},
	}
	for _, c := range cases {
		if got := taskNeedsMonitor(c.task, c.tags); got != c.want {
			t.Errorf("%s: taskNeedsMonitor = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRespawnGate(t *testing.T) {
	g := newRespawnGate(50 * time.Millisecond)
	if !g.allow("a") {
		t.Fatal("first allow(a) should be true")
	}
	if g.allow("a") {
		t.Fatal("second allow(a) within window should be false (debounced)")
	}
	if !g.allow("b") {
		t.Fatal("allow(b) should be true — debounce is per-slug")
	}
	time.Sleep(60 * time.Millisecond)
	if !g.allow("a") {
		t.Fatal("allow(a) after window should be true again")
	}

	var nilGate *respawnGate
	if !nilGate.allow("x") {
		t.Fatal("nil gate should allow")
	}
}

func insertMonitorTask(t *testing.T, db *sql.DB, slug, status, worktree string, tags ...string) {
	t.Helper()
	now := "2026-05-28T10:00:00Z"
	var wt any
	if worktree != "" {
		wt = worktree
	}
	// The tasks CHECK constraint requires in-progress claude tasks to carry a
	// session_id; give one (slug-derived → unique).
	var sid any
	if status == "in-progress" {
		sid = slug + "-session"
	}
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, kind, priority, work_dir, worktree_path, session_id, created_at, updated_at, session_provider)
		 VALUES (?, ?, ?, 'regular', 'medium', '/tmp', ?, ?, ?, ?, 'claude')`,
		slug, slug, status, wt, sid, now, now,
	); err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if err := flowdb.AddTaskTag(db, slug, tag); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMonitorReconcilerConverges(t *testing.T) {
	root, db := testRootDB(t)
	t.Setenv("FLOW_ROOT", root)
	srv := New(Config{DB: db, FlowRoot: root, Version: "test"})
	t.Cleanup(func() {
		for _, slug := range srv.inboxMonitors.runningSlugs() {
			srv.inboxMonitors.stop(slug)
		}
	})

	insertMonitorTask(t, db, "slack-task", "backlog", "", "slack-reply")
	insertMonitorTask(t, db, "ghpr-task", "in-progress", "", "gh-pr:o/r#1")
	insertMonitorTask(t, db, "branch-task", "backlog", "/tmp/wt") // worktree but no PR → not monitored
	insertMonitorTask(t, db, "plain-task", "backlog", "")
	insertMonitorTask(t, db, "done-ghpr", "done", "", "gh-pr:o/r#2")

	r := newMonitorReconciler(srv)
	r.tick()

	want := map[string]bool{
		"slack-task":  true,
		"ghpr-task":   true,
		"branch-task": false,
		"plain-task":  false,
		"done-ghpr":   false,
	}
	for slug, w := range want {
		if got := srv.inboxMonitors.running(slug); got != w {
			t.Errorf("after tick: running(%q) = %v, want %v", slug, got, w)
		}
	}

	// When a monitored task finishes, the next tick must stop its monitor.
	if _, err := db.Exec(`UPDATE tasks SET status = 'done' WHERE slug = 'slack-task'`); err != nil {
		t.Fatal(err)
	}
	r.tick()
	if srv.inboxMonitors.running("slack-task") {
		t.Error("monitor for slack-task should stop once the task is done")
	}
	if !srv.inboxMonitors.running("ghpr-task") {
		t.Error("ghpr-task monitor should still be running")
	}
}
