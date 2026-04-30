package app

import (
	"database/sql"
	"flow/internal/flowdb"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withTempFlowRoot points FLOW_ROOT at a fresh tempdir and returns the
// root. It does NOT open flow.db — callers that need a live DB should
// use showListEditDB(t) which also creates the standard subdirs.
//
// This single-value form is shared across Phase 2 test files (agent A's
// cmd_add_test.go relies on it); tests that also need a DB handle go
// through showListEditDB.
func withTempFlowRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FLOW_ROOT", dir)
	return dir
}

// showListEditDB is the test harness used by cmd_show, cmd_list, and
// cmd_edit tests. It sets FLOW_ROOT, creates projects/ and tasks/
// subtrees, opens flow.db, and returns (root, db).
func showListEditDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	dir := withTempFlowRoot(t)
	for _, sub := range []string{"projects", "tasks"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	db, err := flowdb.OpenDB(filepath.Join(dir, "flow.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return dir, db
}

// captureStdout redirects os.Stdout and os.Stderr during f and returns
// combined output.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	oldOut := os.Stdout
	oldErr := os.Stderr
	os.Stdout = wOut
	os.Stderr = wErr
	done := make(chan string, 2)
	go func() {
		b, _ := io.ReadAll(rOut)
		done <- string(b)
	}()
	go func() {
		b, _ := io.ReadAll(rErr)
		done <- string(b)
	}()
	f()
	wOut.Close()
	wErr.Close()
	os.Stdout = oldOut
	os.Stderr = oldErr
	a := <-done
	b := <-done
	return a + b
}

func TestCmdShowTaskHappyPath(t *testing.T) {
	root, db := showListEditDB(t)
	insertProject(t, db, "demo", "Demo", filepath.Join(root, "repo"), "high")
	insertTask(t, db, "auth-fix", "Fix auth", "backlog", "high", filepath.Join(root, "repo"), "demo")

	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "auth-fix"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "slug:          auth-fix") {
		t.Errorf("missing slug line; out=%q", out)
	}
	if !strings.Contains(out, "project:       demo") {
		t.Errorf("missing project line; out=%q", out)
	}
	if !strings.Contains(out, "priority:      high") {
		t.Errorf("missing priority; out=%q", out)
	}
	if !strings.Contains(out, "session_id:            (not bootstrapped)") {
		t.Errorf("missing session_id stub; out=%q", out)
	}
	wantBrief := filepath.Join(root, "tasks", "auth-fix", "brief.md")
	if !strings.Contains(out, "brief:         "+wantBrief) {
		t.Errorf("missing brief path; out=%q", out)
	}
	if !strings.Contains(out, "updates:       (none)") {
		t.Errorf("missing no-updates line; out=%q", out)
	}
}

func TestCmdShowTaskFloating(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "readme-pass", "Read", "backlog", "low", filepath.Join(root, "tasks", "readme-pass", "workspace"), nil)
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "readme-pass"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "project:       (floating)") {
		t.Errorf("expected floating marker; out=%q", out)
	}
}

func TestCmdShowTaskDefaultsFromEnv(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "env-task", "E", "backlog", "medium", filepath.Join(root, "x"), nil)
	os.Setenv("FLOW_TASK", "env-task")
	t.Cleanup(func() { os.Unsetenv("FLOW_TASK") })
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "slug:          env-task") {
		t.Errorf("missing slug; out=%q", out)
	}
}

func TestCmdShowTaskMissingDefault(t *testing.T) {
	_, _ = showListEditDB(t)
	os.Unsetenv("FLOW_TASK")
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task"}); rc != 1 {
			t.Errorf("rc=%d, want 1", rc)
		}
	})
	if !strings.Contains(out, "$FLOW_TASK") {
		t.Errorf("missing env hint; out=%q", out)
	}
}

func TestCmdShowTaskUnknown(t *testing.T) {
	_, _ = showListEditDB(t)
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "nope"}); rc != 1 {
			t.Errorf("rc=%d, want 1", rc)
		}
	})
	if !strings.Contains(out, "no task matching") {
		t.Errorf("missing error; out=%q", out)
	}
}

func TestCmdShowTaskNoSubstringMatch(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "auth-one", "A1", "backlog", "medium", filepath.Join(root, "x"), nil)
	insertTask(t, db, "auth-two", "A2", "backlog", "medium", filepath.Join(root, "y"), nil)
	// Substring "auth" should NOT match — exact only.
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "auth"}); rc != 1 {
			t.Errorf("rc=%d, want 1", rc)
		}
	})
	if !strings.Contains(out, "no task matching") {
		t.Errorf("expected no match error; out=%q", out)
	}
}

func TestCmdShowTaskStaleMarker(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "slow", "Slow", "in-progress", "high", filepath.Join(root, "x"), nil)
	// Back-date updated_at to 5 days ago.
	old := time.Now().Add(-5 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`UPDATE tasks SET updated_at = ? WHERE slug = ?`, old, "slow"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "slow"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "stale") {
		t.Errorf("missing stale marker; out=%q", out)
	}
}

func TestCmdShowTaskUpdatesListedSorted(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "noted", "N", "in-progress", "medium", filepath.Join(root, "x"), nil)
	updatesDir := filepath.Join(root, "tasks", "noted", "updates")
	if err := os.MkdirAll(updatesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	names := []string{"2026-04-02-zeta.md", "2026-04-01-alpha.md", "2026-04-03-mid.md"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(updatesDir, n), []byte("."), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "noted"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	// Verify all three appear in alphabetical order.
	idxA := strings.Index(out, "2026-04-01-alpha.md")
	idxZ := strings.Index(out, "2026-04-02-zeta.md")
	idxM := strings.Index(out, "2026-04-03-mid.md")
	if idxA < 0 || idxZ < 0 || idxM < 0 {
		t.Fatalf("missing one or more update paths; out=%q", out)
	}
	if !(idxA < idxZ && idxZ < idxM) {
		t.Errorf("updates not sorted ascending: %d, %d, %d", idxA, idxZ, idxM)
	}
}

func TestCmdShowTaskWorkdirAnnotation(t *testing.T) {
	root, db := showListEditDB(t)
	wd := filepath.Join(root, "code", "foo")
	if err := flowdb.UpsertWorkdir(db, wd, "foo-repo", "", "git@github.com:me/foo.git"); err != nil {
		t.Fatal(err)
	}
	insertTask(t, db, "t-annot", "T", "backlog", "medium", wd, nil)
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "t-annot"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "known: foo-repo") {
		t.Errorf("expected workdir nickname annotation; out=%q", out)
	}
	if !strings.Contains(out, "origin: git@github.com:me/foo.git") {
		t.Errorf("expected workdir remote annotation; out=%q", out)
	}
}

func TestCmdShowTaskWaitingOn(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "waiter", "W", "in-progress", "medium", filepath.Join(root, "x"), nil)
	if _, err := db.Exec(`UPDATE tasks SET waiting_on = ? WHERE slug = ?`, "Alice review", "waiter"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "waiter"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "waiting_on:    Alice review") {
		t.Errorf("missing waiting_on line; out=%q", out)
	}
}

func TestCmdShowTaskArchivedStillShown(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "ghost", "G", "done", "low", filepath.Join(root, "x"), nil)
	if _, err := db.Exec(`UPDATE tasks SET archived_at = ? WHERE slug = ?`, flowdb.NowISO(), "ghost"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "ghost"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "(archived)") {
		t.Errorf("expected (archived) marker; out=%q", out)
	}
	if !strings.Contains(out, "archived:") {
		t.Errorf("expected archived: line; out=%q", out)
	}
}

// ---------- project path ----------

func TestCmdShowProjectHappyPath(t *testing.T) {
	root, db := showListEditDB(t)
	insertProject(t, db, "biglift", "Big Lift", filepath.Join(root, "repo"), "high")
	insertTask(t, db, "bl-one", "One", "in-progress", "medium", filepath.Join(root, "repo"), "biglift")
	insertTask(t, db, "bl-two", "Two", "backlog", "low", filepath.Join(root, "repo"), "biglift")
	insertTask(t, db, "bl-three", "Three", "done", "low", filepath.Join(root, "repo"), "biglift")
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"project", "biglift"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "slug:        biglift") {
		t.Errorf("missing slug; out=%q", out)
	}
	if !strings.Contains(out, "3 total") {
		t.Errorf("missing total count; out=%q", out)
	}
	if !strings.Contains(out, "1 in-progress, 1 backlog, 1 done") {
		t.Errorf("missing breakdown; out=%q", out)
	}
}

func TestCmdShowProjectDefaultsFromEnv(t *testing.T) {
	root, db := showListEditDB(t)
	insertProject(t, db, "envproj", "E", filepath.Join(root, "x"), "medium")
	os.Setenv("FLOW_PROJECT", "envproj")
	t.Cleanup(func() { os.Unsetenv("FLOW_PROJECT") })
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"project"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "slug:        envproj") {
		t.Errorf("missing slug; out=%q", out)
	}
}

func TestCmdShowProjectUnknown(t *testing.T) {
	_, _ = showListEditDB(t)
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"project", "nope"}); rc != 1 {
			t.Errorf("rc=%d, want 1", rc)
		}
	})
	if !strings.Contains(out, "no project matching") {
		t.Errorf("missing error; out=%q", out)
	}
}

func TestCmdShowBadSub(t *testing.T) {
	_, _ = showListEditDB(t)
	if rc := cmdShow([]string{"nope"}); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
	if rc := cmdShow(nil); rc != 2 {
		t.Errorf("rc=%d on empty, want 2", rc)
	}
}

func TestCmdShowTaskDueDate(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "urgent", "Urgent", "in-progress", "high", filepath.Join(root, "x"), nil)
	// Set due date to 2 days from now.
	due := time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	if _, err := db.Exec(`UPDATE tasks SET due_date = ? WHERE slug = ?`, due, "urgent"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "urgent"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "due:") {
		t.Errorf("missing due: line; out=%q", out)
	}
	if !strings.Contains(out, due) {
		t.Errorf("missing due date value; out=%q", out)
	}
	if !strings.Contains(out, "(in 2 days)") {
		t.Errorf("missing due date info; out=%q", out)
	}
}

func TestCmdShowTaskOverdue(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "late", "Late", "in-progress", "high", filepath.Join(root, "x"), nil)
	// Set due date to 3 days ago.
	due := time.Now().AddDate(0, 0, -3).Format("2006-01-02")
	if _, err := db.Exec(`UPDATE tasks SET due_date = ? WHERE slug = ?`, due, "late"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "late"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "overdue by 3 days") {
		t.Errorf("missing overdue info; out=%q", out)
	}
}

func TestCmdShowTaskTemporalSummary(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "tempo", "Tempo", "in-progress", "medium", filepath.Join(root, "x"), nil)
	// Set status_changed_at to 5 days ago and due date to tomorrow.
	fiveDaysAgo := time.Now().Add(-5 * 24 * time.Hour).Format(time.RFC3339)
	due := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	if _, err := db.Exec(`UPDATE tasks SET status_changed_at = ?, due_date = ? WHERE slug = ?`,
		fiveDaysAgo, due, "tempo"); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "tempo"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "temporal:") {
		t.Errorf("missing temporal: line; out=%q", out)
	}
	if !strings.Contains(out, "in-progress for 5 days") {
		t.Errorf("missing days-in-status; out=%q", out)
	}
	if !strings.Contains(out, "due tomorrow") {
		t.Errorf("missing due info; out=%q", out)
	}
}

func TestCmdShowTaskNoDueDate(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "nodue", "NoDue", "backlog", "medium", filepath.Join(root, "x"), nil)
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "nodue"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if strings.Contains(out, "due:") {
		t.Errorf("should not show due: line when no due date; out=%q", out)
	}
}

func TestCmdShowTaskConfigurableStaleness(t *testing.T) {
	root, db := showListEditDB(t)
	insertTask(t, db, "cfg-task", "CT", "in-progress", "medium", filepath.Join(root, "x"), nil)
	// Set updated_at to 2 days ago — not stale with default threshold (3)
	// but stale with threshold 1.
	twoDaysAgo := time.Now().Add(-2 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`UPDATE tasks SET updated_at = ? WHERE slug = ?`, twoDaysAgo, "cfg-task"); err != nil {
		t.Fatal(err)
	}

	// Default threshold (3 days) — should NOT show stale marker.
	out := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "cfg-task"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if strings.Contains(out, "⚠ stale") {
		t.Errorf("should not be stale at default threshold; out=%q", out)
	}

	// Set FLOW_STALE_DAYS=1 — should now show stale marker.
	t.Setenv("FLOW_STALE_DAYS", "1")
	out2 := captureStdout(t, func() {
		if rc := cmdShow([]string{"task", "cfg-task"}); rc != 0 {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out2, "⚠ stale") {
		t.Errorf("should be stale with threshold 1; out=%q", out2)
	}
}
