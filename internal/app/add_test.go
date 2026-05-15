package app

import (
	"database/sql"
	"flow/internal/flowdb"
	"os"
	"path/filepath"
	"testing"
)

// setupFlowRoot sets up a tempdir FLOW_ROOT and runs cmdInit to create the
// DB + tree. Returns the root path. Uses the init-test helper
// `initTempFlowRoot` which also redirects $HOME so the skill install lands
// inside the test sandbox.
func setupFlowRoot(t *testing.T) string {
	t.Helper()
	t.Setenv("FLOW_AGENT_BACKEND", "claude")
	root := initTempFlowRoot(t)
	if rc := cmdInit(nil); rc != 0 {
		t.Fatalf("cmdInit rc=%d", rc)
	}
	return root
}

func openFlowDB(t *testing.T) *sql.DB {
	t.Helper()
	path, err := flowDBPath()
	if err != nil {
		t.Fatal(err)
	}
	db, err := flowdb.OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// ---------- add project ----------

func TestCmdAddProjectHappyPath(t *testing.T) {
	root := setupFlowRoot(t)
	wd := t.TempDir()

	rc := cmdAdd([]string{"project", "Auth Service", "--work-dir", wd, "--priority", "high"})
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}

	db := openFlowDB(t)
	p, err := flowdb.GetProject(db, "auth-service")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if p.Name != "Auth Service" {
		t.Errorf("name: got %q", p.Name)
	}
	if p.Priority != "high" {
		t.Errorf("priority: got %q", p.Priority)
	}
	if p.WorkDir != wd {
		t.Errorf("work_dir: got %q, want %q", p.WorkDir, wd)
	}
	if p.Status != "active" {
		t.Errorf("status: got %q", p.Status)
	}

	// brief.md and updates/ dirs should exist.
	briefPath := filepath.Join(root, "projects", "auth-service", "brief.md")
	if _, err := os.Stat(briefPath); err != nil {
		t.Errorf("brief.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "projects", "auth-service", "updates")); err != nil {
		t.Errorf("updates/ dir missing: %v", err)
	}

	// workdir auto-registered.
	if _, err := flowdb.GetWorkdir(db, wd); err != nil {
		t.Errorf("workdir not auto-registered: %v", err)
	}
}

func TestCmdAddProjectRequiresWorkDir(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"project", "NoWorkDir"}); rc == 0 {
		t.Errorf("expected non-zero rc when --work-dir is missing")
	}
}

func TestCmdAddProjectMkdirCreatesMissing(t *testing.T) {
	setupFlowRoot(t)
	parent := t.TempDir()
	missing := filepath.Join(parent, "new-proj")

	rc := cmdAdd([]string{"project", "New Proj", "--work-dir", missing, "--mkdir"})
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if _, err := os.Stat(missing); err != nil {
		t.Errorf("work_dir not created: %v", err)
	}
}

func TestCmdAddProjectMissingWorkDirFailsWithoutMkdir(t *testing.T) {
	setupFlowRoot(t)
	parent := t.TempDir()
	missing := filepath.Join(parent, "not-there")
	if rc := cmdAdd([]string{"project", "X", "--work-dir", missing}); rc == 0 {
		t.Errorf("expected rc!=0 for missing work-dir without --mkdir")
	}
}

func TestCmdAddProjectCollisionAvoidance(t *testing.T) {
	setupFlowRoot(t)
	wd1 := t.TempDir()
	wd2 := t.TempDir()

	if rc := cmdAdd([]string{"project", "Same Name", "--work-dir", wd1}); rc != 0 {
		t.Fatalf("first rc=%d", rc)
	}
	if rc := cmdAdd([]string{"project", "Same Name", "--work-dir", wd2}); rc != 0 {
		t.Fatalf("second rc=%d", rc)
	}
	db := openFlowDB(t)
	if _, err := flowdb.GetProject(db, "same-name"); err != nil {
		t.Errorf("same-name missing: %v", err)
	}
	if _, err := flowdb.GetProject(db, "same-name-2"); err != nil {
		t.Errorf("same-name-2 missing: %v", err)
	}
}

func TestCmdAddProjectBadPriority(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"project", "X", "--work-dir", wd, "--priority", "urgent"}); rc == 0 {
		t.Errorf("expected rc!=0 for bad priority")
	}
}

// ---------- add task ----------

func TestCmdAddTaskFloating(t *testing.T) {
	root := setupFlowRoot(t)

	rc := cmdAdd([]string{"task", "Fix bug"})
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "fix-bug")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.ProjectSlug.Valid {
		t.Errorf("expected floating task, got project %q", task.ProjectSlug.String)
	}
	expectedWorkDir := filepath.Join(root, "tasks", "fix-bug", "workspace")
	if task.WorkDir != expectedWorkDir {
		t.Errorf("work_dir: got %q, want %q", task.WorkDir, expectedWorkDir)
	}
	if _, err := os.Stat(expectedWorkDir); err != nil {
		t.Errorf("workspace dir not created: %v", err)
	}
	// brief.md and updates/ present.
	if _, err := os.Stat(filepath.Join(root, "tasks", "fix-bug", "brief.md")); err != nil {
		t.Errorf("brief.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tasks", "fix-bug", "updates")); err != nil {
		t.Errorf("updates/ missing: %v", err)
	}
}

func TestCmdAddTaskInheritsProjectWorkDir(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()

	if rc := cmdAdd([]string{"project", "Parent", "--work-dir", wd}); rc != 0 {
		t.Fatalf("add project rc=%d", rc)
	}
	if rc := cmdAdd([]string{"task", "Child Task", "--project", "parent"}); rc != 0 {
		t.Fatalf("add task rc=%d", rc)
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "child-task")
	if err != nil {
		t.Fatal(err)
	}
	if task.WorkDir != wd {
		t.Errorf("inherited work_dir: got %q, want %q", task.WorkDir, wd)
	}
	if !task.ProjectSlug.Valid || task.ProjectSlug.String != "parent" {
		t.Errorf("project_slug: got %+v", task.ProjectSlug)
	}
}

func TestCmdAddTaskOverridesProjectWorkDir(t *testing.T) {
	setupFlowRoot(t)
	projWD := t.TempDir()
	overrideWD := t.TempDir()

	if rc := cmdAdd([]string{"project", "P", "--work-dir", projWD}); rc != 0 {
		t.Fatalf("add project rc=%d", rc)
	}
	if rc := cmdAdd([]string{"task", "Child", "--project", "p", "--work-dir", overrideWD}); rc != 0 {
		t.Fatalf("add task rc=%d", rc)
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "child")
	if err != nil {
		t.Fatal(err)
	}
	if task.WorkDir != overrideWD {
		t.Errorf("expected override %q, got %q", overrideWD, task.WorkDir)
	}
}

func TestCmdAddTaskInvalidProject(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "X", "--project", "nope"}); rc == 0 {
		t.Error("expected rc!=0 for unknown project")
	}
}

func TestCmdAddTaskCollisionAvoidance(t *testing.T) {
	setupFlowRoot(t)
	for i := 0; i < 3; i++ {
		if rc := cmdAdd([]string{"task", "Dup Task"}); rc != 0 {
			t.Fatalf("add task iter %d rc=%d", i, rc)
		}
	}
	db := openFlowDB(t)
	for _, s := range []string{"dup-task", "dup-task-2", "dup-task-3"} {
		if _, err := flowdb.GetTask(db, s); err != nil {
			t.Errorf("slug %q missing: %v", s, err)
		}
	}
}

func TestCmdAddTaskWorkDirMkdir(t *testing.T) {
	setupFlowRoot(t)
	parent := t.TempDir()
	missing := filepath.Join(parent, "fresh-subdir")
	if rc := cmdAdd([]string{"task", "Task", "--work-dir", missing, "--mkdir"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if _, err := os.Stat(missing); err != nil {
		t.Errorf("work-dir not created: %v", err)
	}
}

func TestCmdAddUnknownSubcommand(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"bogus"}); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
	if rc := cmdAdd(nil); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
}

func TestCmdAddTaskWithDueDate(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "Due Task", "--due", "2026-06-01"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "due-task")
	if err != nil {
		t.Fatal(err)
	}
	if !task.DueDate.Valid || task.DueDate.String != "2026-06-01" {
		t.Errorf("due_date: got %+v, want 2026-06-01", task.DueDate)
	}
	// status_changed_at should also be set.
	if !task.StatusChangedAt.Valid || task.StatusChangedAt.String == "" {
		t.Errorf("status_changed_at not set on new task: %+v", task.StatusChangedAt)
	}
}

func TestCmdAddTaskWithBadDueDate(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "Bad Due", "--due", "garble"}); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
}

func TestCmdAddTaskWithoutDueDate(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "No Due"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "no-due")
	if err != nil {
		t.Fatal(err)
	}
	if task.DueDate.Valid {
		t.Errorf("expected NULL due_date, got %+v", task.DueDate)
	}
}
