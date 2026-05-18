package app

import (
	"database/sql"
	"flow/internal/flowdb"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCmdListTasksEmpty(t *testing.T) {
	_, _ = showListEditDB(t)
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "(no tasks)") {
		t.Errorf("expected no-tasks msg; out=%q", out)
	}
}

func TestCmdListTasksMixedStatusFilter(t *testing.T) {
	root, db := showListEditDB(t)
	insertProject(t, db, "demo", "Demo", filepath.Join(root, "repo"), "medium")
	insertTask(t, db, "ip", "In-prog", "in-progress", "high", filepath.Join(root, "repo"), "demo")
	insertTask(t, db, "bl", "Backlog", "backlog", "medium", filepath.Join(root, "repo"), "demo")
	insertTask(t, db, "dn", "Done", "done", "low", filepath.Join(root, "repo"), "demo")

	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks", "--status", "in-progress"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "[IP]") || !strings.Contains(out, "ip") {
		t.Errorf("expected only [IP] row; out=%q", out)
	}
	if strings.Contains(out, "[BL]") || strings.Contains(out, "[DN]") {
		t.Errorf("unexpected rows leaked; out=%q", out)
	}
}

func TestCmdListTasksPrioritySort(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "c-low", "c", "backlog", "low", filepath.Join(root, "x"), nil)
	insertTask(t, db, "a-high", "a", "backlog", "high", filepath.Join(root, "x"), nil)
	insertTask(t, db, "b-med", "b", "backlog", "medium", filepath.Join(root, "x"), nil)

	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	ih := strings.Index(out, "a-high")
	im := strings.Index(out, "b-med")
	il := strings.Index(out, "c-low")
	if ih < 0 || im < 0 || il < 0 {
		t.Fatalf("missing rows; out=%q", out)
	}
	if !(ih < im && im < il) {
		t.Errorf("priority order wrong: high=%d, med=%d, low=%d", ih, im, il)
	}
}

func TestCmdListTasksStaleMarker(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "ancient", "A", "in-progress", "high", filepath.Join(root, "x"), nil)
	old := time.Now().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`UPDATE tasks SET updated_at = ? WHERE slug = ?`, old, "ancient"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "stale (") {
		t.Errorf("expected stale marker; out=%q", out)
	}
}

func TestCmdListTasksWaitingOn(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "waiter", "W", "in-progress", "high", filepath.Join(root, "x"), nil)
	if _, err := db.Exec(`UPDATE tasks SET waiting_on = ? WHERE slug = ?`, "Alice", "waiter"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "[waiting: Alice]") {
		t.Errorf("expected waiting annotation; out=%q", out)
	}
}

func TestCmdListTasksArchivedHiddenByDefault(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "alive", "A", "backlog", "high", filepath.Join(root, "x"), nil)
	// Use backlog (not done) for the archived row so this test isolates
	// archived-visibility from done-visibility.
	insertTask(t, db, "dead", "D", "backlog", "low", filepath.Join(root, "x"), nil)
	if _, err := db.Exec(`UPDATE tasks SET archived_at = ? WHERE slug = ?`, flowdb.NowISO(), "dead"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if strings.Contains(out, "dead") {
		t.Errorf("archived row leaked: %q", out)
	}
	out2 := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks", "--include-archived"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out2, "dead") {
		t.Errorf("archived row missing with --include-archived: %q", out2)
	}
	if !strings.Contains(out2, "(archived)") {
		t.Errorf("archived marker missing: %q", out2)
	}
}

func TestCmdListTasksDoneHiddenByDefault(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "active", "A", "in-progress", "high", filepath.Join(root, "x"), nil)
	insertTask(t, db, "shipped", "S", "done", "high", filepath.Join(root, "x"), nil)

	// Default: done hidden.
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "active") {
		t.Errorf("active task missing: %q", out)
	}
	if strings.Contains(out, "shipped") {
		t.Errorf("done task leaked into default list: %q", out)
	}

	// --include-done: shows everything.
	out = captureStdout(t, func() {
		if rc := cmdList([]string{"tasks", "--include-done"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "shipped") {
		t.Errorf("done task missing with --include-done: %q", out)
	}

	// Explicit --status done: shows only done (regardless of default).
	out = captureStdout(t, func() {
		if rc := cmdList([]string{"tasks", "--status", "done"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "shipped") {
		t.Errorf("--status done should show done: %q", out)
	}
	if strings.Contains(out, "active") {
		t.Errorf("--status done should not show in-progress: %q", out)
	}
}

func TestCmdListTasksSinceToday(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "today-task", "A", "backlog", "high", filepath.Join(root, "x"), nil)
	// Set it to 12 hours ago so it's still "today" in any reasonable timezone.
	recent := time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`UPDATE tasks SET updated_at = ? WHERE slug = ?`, recent, "today-task"); err != nil {
		t.Fatal(err)
	}
	insertTask(t, db, "ancient", "B", "backlog", "high", filepath.Join(root, "x"), nil)
	old := time.Now().Add(-72 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`UPDATE tasks SET updated_at = ? WHERE slug = ?`, old, "ancient"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks", "--since", "today"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "today-task") {
		t.Errorf("expected today-task; out=%q", out)
	}
	if strings.Contains(out, "ancient") {
		t.Errorf("unexpected old row; out=%q", out)
	}
}

func TestCmdListTasksSinceMonday(t *testing.T) {
	// Just smoke-test that --since monday parses and runs; the exact
	// filtering semantics are covered by parseSince tests.
	_, _ = showListEditDB(t)
	if rc := cmdList([]string{"tasks", "--since", "monday"}); rc != 0 {
		t.Errorf("rc=%d", rc)
	}
}

func TestCmdListTasksSince7d(t *testing.T) {
	_, _ = showListEditDB(t)
	if rc := cmdList([]string{"tasks", "--since", "7d"}); rc != 0 {
		t.Errorf("rc=%d", rc)
	}
}

func TestCmdListTasksSinceDate(t *testing.T) {
	_, _ = showListEditDB(t)
	if rc := cmdList([]string{"tasks", "--since", "2020-01-01"}); rc != 0 {
		t.Errorf("rc=%d", rc)
	}
}

func TestCmdListTasksSinceBad(t *testing.T) {
	_, _ = showListEditDB(t)
	if rc := cmdList([]string{"tasks", "--since", "garble"}); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
}

func TestCmdListTasksProjectFilter(t *testing.T) {
	root, db := showListEditDB(t)
	insertProject(t, db, "p1", "P1", filepath.Join(root, "a"), "medium")
	insertProject(t, db, "p2", "P2", filepath.Join(root, "b"), "medium")
	insertTask(t, db, "t1", "T1", "backlog", "high", filepath.Join(root, "a"), "p1")
	insertTask(t, db, "t2", "T2", "backlog", "high", filepath.Join(root, "b"), "p2")
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks", "--project", "p1"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "t1") {
		t.Errorf("missing t1; out=%q", out)
	}
	if strings.Contains(out, "t2") {
		t.Errorf("unexpected t2; out=%q", out)
	}
}

// ---------- projects ----------

func TestCmdListProjectsEmpty(t *testing.T) {
	_, _ = showListEditDB(t)
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"projects"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "(no projects)") {
		t.Errorf("expected no-projects msg; out=%q", out)
	}
}

func TestCmdListProjectsBreakdown(t *testing.T) {
	root, db := showListEditDB(t)
	insertProject(t, db, "big", "Big", filepath.Join(root, "x"), "high")
	insertTask(t, db, "b1", "B1", "in-progress", "medium", filepath.Join(root, "x"), "big")
	insertTask(t, db, "b2", "B2", "in-progress", "medium", filepath.Join(root, "x"), "big")
	insertTask(t, db, "b3", "B3", "backlog", "medium", filepath.Join(root, "x"), "big")
	insertTask(t, db, "b4", "B4", "done", "low", filepath.Join(root, "x"), "big")
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"projects"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "big") {
		t.Errorf("missing project row; out=%q", out)
	}
	if !strings.Contains(out, "2 IP") || !strings.Contains(out, "1 BL") || !strings.Contains(out, "1 DN") {
		t.Errorf("missing breakdown; out=%q", out)
	}
}

func TestCmdListProjectsArchivedHidden(t *testing.T) {
	root, db := showListEditDB(t)
	insertProject(t, db, "live", "L", filepath.Join(root, "x"), "high")
	insertProject(t, db, "gone", "G", filepath.Join(root, "y"), "low")
	if _, err := db.Exec(`UPDATE projects SET archived_at = ? WHERE slug = ?`, flowdb.NowISO(), "gone"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"projects"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if strings.Contains(out, "gone") {
		t.Errorf("archived leaked; out=%q", out)
	}
	out2 := captureStdout(t, func() {
		if rc := cmdList([]string{"projects", "--include-archived"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out2, "gone") {
		t.Errorf("missing archived row; out=%q", out2)
	}
	if !strings.Contains(out2, "(archived)") {
		t.Errorf("missing archived marker; out=%q", out2)
	}
}

func TestCmdListProjectsStatusFilter(t *testing.T) {
	root, db := showListEditDB(t)
	insertProject(t, db, "active-p", "A", filepath.Join(root, "x"), "high")
	insertProject(t, db, "done-p", "D", filepath.Join(root, "y"), "low")
	if _, err := db.Exec(`UPDATE projects SET status = 'done' WHERE slug = ?`, "done-p"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"projects", "--status", "active"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "active-p") || strings.Contains(out, "done-p") {
		t.Errorf("status filter failed; out=%q", out)
	}
}

func TestCmdListBadSub(t *testing.T) {
	_, _ = showListEditDB(t)
	if rc := cmdList(nil); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
	if rc := cmdList([]string{"nope"}); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
}

// parseSince unit tests exercise the date logic directly without needing
// to stand up a DB. Kept here next to the command that uses it.
func TestParseSince(t *testing.T) {
	// Fixed "now" on a Wednesday: 2026-04-15 14:00 UTC.
	now := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)

	cases := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{"today", time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC), false},
		{"monday", time.Date(2026, 4, 13, 0, 0, 0, 0, time.UTC), false}, // Mon same week
		{"7d", now.AddDate(0, 0, -7), false},
		{"0d", now, false},
		{"2020-01-01", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"wat", time.Time{}, true},
	}
	for _, c := range cases {
		got, err := parseSince(c.in, now)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseSince(%q): expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSince(%q): unexpected error %v", c.in, err)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("parseSince(%q): got %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCmdListPlaybooks(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	wd := t.TempDir()
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "alpha", Name: "Alpha", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "beta", Name: "Beta", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}

	out := captureShowStdout(t, func() {
		if rc := cmdList([]string{"playbooks"}); rc != 0 {
			t.Fatal()
		}
	})
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Errorf("expected both playbooks, got:\n%s", out)
	}
}

func TestCmdListPlaybooksFiltersByProject(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"project", "P", "--slug", "p1", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}
	db := openFlowDB(t)
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{
		Slug:        "in-p1",
		Name:        "In",
		WorkDir:     wd,
		ProjectSlug: sql.NullString{String: "p1", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "floating", Name: "F", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}

	out := captureShowStdout(t, func() {
		if rc := cmdList([]string{"playbooks", "--project", "p1"}); rc != 0 {
			t.Fatal()
		}
	})
	if !strings.Contains(out, "in-p1") {
		t.Errorf("expected in-p1 playbook, got:\n%s", out)
	}
	if strings.Contains(out, "floating") {
		t.Errorf("floating playbook should be filtered out, got:\n%s", out)
	}
}

func TestListTasksDefaultExcludesPlaybookRuns(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	wd := t.TempDir()

	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "pb", Name: "PB", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	insertTask(t, db, "regular-1", "Regular 1", "in-progress", "high", wd, nil)
	now := flowdb.NowISO()
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, kind, playbook_slug, priority, work_dir, session_id, created_at, updated_at)
		 VALUES ('pb--2026-04-30-10-30', 'pb run', 'in-progress', 'playbook_run', 'pb', 'medium', ?, ?, ?, ?)`,
		wd, fakeSessionID("pb--2026-04-30-10-30"), now, now,
	); err != nil {
		t.Fatal(err)
	}

	out := captureShowStdout(t, func() {
		if rc := cmdList([]string{"tasks"}); rc != 0 {
			t.Fatal()
		}
	})
	if !strings.Contains(out, "regular-1") {
		t.Errorf("regular task missing:\n%s", out)
	}
	if strings.Contains(out, "pb--2026-04-30-10-30") {
		t.Errorf("playbook run should be hidden by default:\n%s", out)
	}
}

func TestListTasksKindOverride(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	wd := t.TempDir()
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "pb", Name: "PB", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	now := flowdb.NowISO()
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, kind, playbook_slug, priority, work_dir, session_id, created_at, updated_at)
		 VALUES ('pb--2026-04-30-10-30', 'r', 'in-progress', 'playbook_run', 'pb', 'medium', ?, ?, ?, ?)`,
		wd, fakeSessionID("pb--2026-04-30-10-30"), now, now,
	); err != nil {
		t.Fatal(err)
	}
	out := captureShowStdout(t, func() {
		if rc := cmdList([]string{"tasks", "--kind", "playbook_run"}); rc != 0 {
			t.Fatal()
		}
	})
	if !strings.Contains(out, "pb--2026-04-30-10-30") {
		t.Errorf("--kind playbook_run should show runs:\n%s", out)
	}
}

func TestListTasksKindAll(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	wd := t.TempDir()
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "pb", Name: "PB", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	insertTask(t, db, "regular-1", "Regular 1", "in-progress", "high", wd, nil)
	now := flowdb.NowISO()
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, kind, playbook_slug, priority, work_dir, session_id, created_at, updated_at)
		 VALUES ('pb--run', 'r', 'in-progress', 'playbook_run', 'pb', 'medium', ?, ?, ?, ?)`,
		wd, fakeSessionID("pb--run"), now, now,
	); err != nil {
		t.Fatal(err)
	}
	out := captureShowStdout(t, func() {
		if rc := cmdList([]string{"tasks", "--kind", "all"}); rc != 0 {
			t.Fatal()
		}
	})
	if !strings.Contains(out, "regular-1") || !strings.Contains(out, "pb--run") {
		t.Errorf("--kind all should show both:\n%s", out)
	}
}

func TestCmdListRuns(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	wd := t.TempDir()
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "p1", Name: "P1", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "p2", Name: "P2", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	now := flowdb.NowISO()
	for _, slug := range []string{"p1--2026-04-30-10-30", "p1--2026-04-30-11-00"} {
		if _, err := db.Exec(
			`INSERT INTO tasks (slug, name, status, kind, playbook_slug, priority, work_dir, session_id, created_at, updated_at)
			 VALUES (?, ?, 'in-progress', 'playbook_run', 'p1', 'medium', ?, ?, ?, ?)`,
			slug, slug, wd, fakeSessionID(slug), now, now,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, kind, playbook_slug, priority, work_dir, session_id, created_at, updated_at)
		 VALUES ('p2--2026-04-30-10-30', 'p2-r', 'done', 'playbook_run', 'p2', 'medium', ?, ?, ?, ?)`,
		wd, fakeSessionID("p2--2026-04-30-10-30"), now, now,
	); err != nil {
		t.Fatal(err)
	}

	// All runs
	out := captureShowStdout(t, func() {
		if rc := cmdList([]string{"runs"}); rc != 0 {
			t.Fatal()
		}
	})
	if !strings.Contains(out, "p1--") || !strings.Contains(out, "p2--") {
		t.Errorf("expected all runs:\n%s", out)
	}

	// Filtered by playbook
	out = captureShowStdout(t, func() {
		if rc := cmdList([]string{"runs", "p1"}); rc != 0 {
			t.Fatal()
		}
	})
	if !strings.Contains(out, "p1--") {
		t.Errorf("expected p1 runs:\n%s", out)
	}
	if strings.Contains(out, "p2--") {
		t.Errorf("p2 runs should be filtered out:\n%s", out)
	}
}

func TestCmdListRunsByStatus(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	wd := t.TempDir()
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "p", Name: "P", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	now := flowdb.NowISO()
	for _, r := range []struct{ slug, status string }{
		{"p--ip", "in-progress"},
		{"p--dn", "done"},
	} {
		var sid any
		if r.status != "backlog" {
			sid = fakeSessionID(r.slug)
		}
		if _, err := db.Exec(
			`INSERT INTO tasks (slug, name, status, kind, playbook_slug, priority, work_dir, session_id, created_at, updated_at)
			 VALUES (?, ?, ?, 'playbook_run', 'p', 'medium', ?, ?, ?, ?)`,
			r.slug, r.slug, r.status, wd, sid, now, now,
		); err != nil {
			t.Fatal(err)
		}
	}
	out := captureShowStdout(t, func() {
		if rc := cmdList([]string{"runs", "--status", "done"}); rc != 0 {
			t.Fatal()
		}
	})
	if !strings.Contains(out, "p--dn") {
		t.Errorf("expected done run:\n%s", out)
	}
	if strings.Contains(out, "p--ip") {
		t.Errorf("in-progress run should be filtered out:\n%s", out)
	}
}

func TestEnsureUpdatesDir(t *testing.T) {
	// Coverage for the helper used by tests and future commands.
	dir, err := ensureUpdatesDir(t.TempDir(), "tasks", "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dir missing: %v", err)
	}
}

func TestCmdListTasksAgeColumn(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "aged", "Aged", "in-progress", "high", filepath.Join(root, "x"), nil)
	// Set status_changed_at to 7 days ago.
	sevenDaysAgo := time.Now().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`UPDATE tasks SET status_changed_at = ? WHERE slug = ?`, sevenDaysAgo, "aged"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "7d") {
		t.Errorf("expected age column with 7d; out=%q", out)
	}
}

func TestCmdListTasksDueColumn(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "due-soon", "DS", "backlog", "high", filepath.Join(root, "x"), nil)
	due := time.Now().AddDate(0, 0, 3).Format("2006-01-02")
	if _, err := db.Exec(`UPDATE tasks SET due_date = ? WHERE slug = ?`, due, "due-soon"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "due 3d") {
		t.Errorf("expected due 3d; out=%q", out)
	}
}

func TestCmdListTasksOverdue(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "overdue-t", "OD", "in-progress", "high", filepath.Join(root, "x"), nil)
	due := time.Now().AddDate(0, 0, -2).Format("2006-01-02")
	if _, err := db.Exec(`UPDATE tasks SET due_date = ? WHERE slug = ?`, due, "overdue-t"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "overdue 2d") {
		t.Errorf("expected overdue marker; out=%q", out)
	}
}

func TestCmdListTasksDueToday(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "due-now", "DN", "in-progress", "high", filepath.Join(root, "x"), nil)
	due := time.Now().Format("2006-01-02")
	if _, err := db.Exec(`UPDATE tasks SET due_date = ? WHERE slug = ?`, due, "due-now"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "due today") {
		t.Errorf("expected due today marker; out=%q", out)
	}
}

func TestCmdListTasksConfigurableStaleness(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "cfg-list", "CL", "in-progress", "high", filepath.Join(root, "x"), nil)
	// Updated 2 days ago — below default threshold of 3.
	twoDaysAgo := time.Now().Add(-2 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`UPDATE tasks SET updated_at = ? WHERE slug = ?`, twoDaysAgo, "cfg-list"); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if strings.Contains(out, "⚠ stale") {
		t.Errorf("should not be stale at default threshold; out=%q", out)
	}

	t.Setenv("FLOW_STALE_DAYS", "1")
	out2 := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out2, "⚠ stale") {
		t.Errorf("should be stale with threshold 1; out=%q", out2)
	}
}

func TestListTagsAggregates(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "lt-1")
	seedTask(t, "lt-2")
	seedTask(t, "lt-3")
	cmdUpdate([]string{"task", "lt-1", "--tag", "urgent", "--tag", "frontend", "--tag", "backend"})
	cmdUpdate([]string{"task", "lt-2", "--tag", "urgent", "--tag", "frontend"})
	cmdUpdate([]string{"task", "lt-3", "--tag", "urgent"})

	// Smoke: command returns 0 with content. Aggregation correctness is
	// covered at the flowdb layer via flowdb.ListAllTags.
	if rc := cmdList([]string{"tags"}); rc != 0 {
		t.Errorf("rc=%d", rc)
	}
}

func TestListTagsEmpty(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdList([]string{"tags"}); rc != 0 {
		t.Errorf("rc=%d on empty DB", rc)
	}
}

func TestListTagsAggregatesValues(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "lt-a")
	seedTask(t, "lt-b")
	cmdUpdate([]string{"task", "lt-a", "--tag", "alpha", "--tag", "beta"})
	cmdUpdate([]string{"task", "lt-b", "--tag", "alpha"})

	db := openFlowDB(t)
	got, err := flowdb.ListAllTags(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2: %+v", len(got), got)
	}
	if got[0].Tag != "alpha" || got[0].Count != 2 {
		t.Errorf("first should be alpha×2, got %+v", got[0])
	}
	if got[1].Tag != "beta" || got[1].Count != 1 {
		t.Errorf("second should be beta×1, got %+v", got[1])
	}
}
