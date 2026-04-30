package app

import (
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
	insertTask(t, db, "dead", "D", "done", "low", filepath.Join(root, "x"), nil)
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
