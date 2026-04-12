package app

import (
	"flow/internal/flowdb"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- parseDueDate ----------

func TestParseDueDate(t *testing.T) {
	// Fixed "now": Wednesday 2026-04-15 14:00 UTC.
	now := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)

	cases := []struct {
		in      string
		want    string // YYYY-MM-DD
		wantErr bool
	}{
		{"today", "2026-04-15", false},
		{"tomorrow", "2026-04-16", false},
		{"monday", "2026-04-20", false},    // next Monday (Wed→Mon = +5)
		{"wednesday", "2026-04-22", false}, // next Wednesday (not today)
		{"friday", "2026-04-17", false},    // next Friday = +2
		{"3d", "2026-04-18", false},
		{"0d", "2026-04-15", false},
		{"2026-12-25", "2026-12-25", false},
		{"TODAY", "2026-04-15", false}, // case insensitive
		{"garble", "", true},
	}
	for _, c := range cases {
		got, err := parseDueDate(c.in, now)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseDueDate(%q): expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDueDate(%q): unexpected error %v", c.in, err)
			continue
		}
		gotStr := got.Format("2006-01-02")
		if gotStr != c.want {
			t.Errorf("parseDueDate(%q): got %s, want %s", c.in, gotStr, c.want)
		}
	}
}

// ---------- cmdDue ----------

func TestCmdDueSetDate(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "alpha", "Alpha", "backlog", "medium", filepath.Join(root, "x"), nil)

	out := captureStdout(t, func() {
		if rc := cmdDue([]string{"alpha", "2026-05-01"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "Set due date for alpha to 2026-05-01") {
		t.Errorf("unexpected output: %q", out)
	}

	// Verify DB.
	task, err := flowdb.GetTask(db, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !task.DueDate.Valid || task.DueDate.String != "2026-05-01" {
		t.Errorf("due_date not set: %+v", task.DueDate)
	}
}

func TestCmdDueClear(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "beta", "Beta", "backlog", "medium", filepath.Join(root, "x"), nil)
	// Set a due date first.
	if _, err := db.Exec(`UPDATE tasks SET due_date='2026-06-01' WHERE slug=?`, "beta"); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if rc := cmdDue([]string{"beta", "--clear"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "Cleared due date for beta") {
		t.Errorf("unexpected output: %q", out)
	}

	task, err := flowdb.GetTask(db, "beta")
	if err != nil {
		t.Fatal(err)
	}
	if task.DueDate.Valid {
		t.Errorf("due_date should be NULL after clear: %+v", task.DueDate)
	}
}

func TestCmdDueNoArgs(t *testing.T) {
	_, _ = showListEditDB(t)
	out := captureStdout(t, func() {
		if rc := cmdDue(nil); rc != 2 {
			t.Errorf("rc=%d, want 2", rc)
		}
	})
	if !strings.Contains(out, "due requires a task ref") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestCmdDueNoDate(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "gamma", "Gamma", "backlog", "medium", filepath.Join(root, "x"), nil)

	out := captureStdout(t, func() {
		if rc := cmdDue([]string{"gamma"}); rc != 2 {
			t.Errorf("rc=%d, want 2", rc)
		}
	})
	if !strings.Contains(out, "due requires a date") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestCmdDueBadDate(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "delta", "Delta", "backlog", "medium", filepath.Join(root, "x"), nil)

	out := captureStdout(t, func() {
		if rc := cmdDue([]string{"delta", "nope"}); rc != 2 {
			t.Errorf("rc=%d, want 2", rc)
		}
	})
	if !strings.Contains(out, "unrecognized date") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestCmdDueUnknownTask(t *testing.T) {
	_, _ = showListEditDB(t)
	out := captureStdout(t, func() {
		if rc := cmdDue([]string{"nope", "today"}); rc != 1 {
			t.Errorf("rc=%d, want 1", rc)
		}
	})
	if !strings.Contains(out, "no task matching") {
		t.Errorf("unexpected output: %q", out)
	}
}
