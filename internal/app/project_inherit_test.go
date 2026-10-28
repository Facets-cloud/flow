package app

import (
	"testing"

	"flow/internal/flowdb"
)

// bindSession attaches a fake harness session to a task so that
// currentSessionTask() resolves to it.
func bindSession(t *testing.T, slug, sid string) {
	t.Helper()
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET session_id=? WHERE slug=?`, sid, slug); err != nil {
		t.Fatalf("bind session: %v", err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)
}

func TestAddTaskInheritsProjectFromBoundSession(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"project", "Fampay", "--work-dir", wd}); rc != 0 {
		t.Fatalf("add project rc=%d", rc)
	}
	if rc := cmdAdd([]string{"task", "Modelling", "--project", "fampay"}); rc != 0 {
		t.Fatalf("add parent rc=%d", rc)
	}
	bindSession(t, "modelling", "sid-parent")

	if rc := cmdAdd([]string{"task", "Child Audit"}); rc != 0 {
		t.Fatalf("add child rc=%d", rc)
	}
	task, err := flowdb.GetTask(openFlowDB(t), "child-audit")
	if err != nil {
		t.Fatal(err)
	}
	if !task.ProjectSlug.Valid || task.ProjectSlug.String != "fampay" {
		t.Errorf("project: got %+v, want fampay", task.ProjectSlug)
	}
	if task.WorkDir != wd {
		t.Errorf("work_dir: got %q, want inherited project dir %q", task.WorkDir, wd)
	}
}

func TestAddTaskExplicitProjectBeatsBoundSession(t *testing.T) {
	setupFlowRoot(t)
	for _, p := range []string{"Alpha", "Beta"} {
		if rc := cmdAdd([]string{"project", p, "--work-dir", t.TempDir()}); rc != 0 {
			t.Fatalf("add project %s rc=%d", p, rc)
		}
	}
	if rc := cmdAdd([]string{"task", "Parent", "--project", "alpha"}); rc != 0 {
		t.Fatal("add parent")
	}
	bindSession(t, "parent", "sid-x")

	if rc := cmdAdd([]string{"task", "Child", "--project", "beta"}); rc != 0 {
		t.Fatal("add child")
	}
	task, _ := flowdb.GetTask(openFlowDB(t), "child")
	if task.ProjectSlug.String != "beta" {
		t.Errorf("project: got %q, want beta", task.ProjectSlug.String)
	}
}

func TestAddTaskBoundToFloatingTaskStaysFloating(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "Loose"}); rc != 0 {
		t.Fatal("add parent")
	}
	bindSession(t, "loose", "sid-loose")

	if rc := cmdAdd([]string{"task", "Child"}); rc != 0 {
		t.Fatal("add child")
	}
	task, _ := flowdb.GetTask(openFlowDB(t), "child")
	if task.ProjectSlug.Valid {
		t.Errorf("expected floating, got %q", task.ProjectSlug.String)
	}
}

func TestUpdateTaskSetAndClearProject(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"project", "Fampay", "--work-dir", wd}); rc != 0 {
		t.Fatal("add project")
	}
	if rc := cmdAdd([]string{"task", "Orphan"}); rc != 0 {
		t.Fatal("add task")
	}
	before, _ := flowdb.GetTask(openFlowDB(t), "orphan")

	if rc := cmdUpdateTask([]string{"orphan", "--project", "fampay"}); rc != 0 {
		t.Fatalf("update --project rc=%d", rc)
	}
	after, _ := flowdb.GetTask(openFlowDB(t), "orphan")
	if after.ProjectSlug.String != "fampay" {
		t.Errorf("project: got %+v", after.ProjectSlug)
	}
	if after.WorkDir != before.WorkDir {
		t.Errorf("work_dir must not change: %q -> %q", before.WorkDir, after.WorkDir)
	}

	if rc := cmdUpdateTask([]string{"orphan", "--clear-project"}); rc != 0 {
		t.Fatalf("update --clear-project rc=%d", rc)
	}
	cleared, _ := flowdb.GetTask(openFlowDB(t), "orphan")
	if cleared.ProjectSlug.Valid {
		t.Errorf("expected cleared, got %q", cleared.ProjectSlug.String)
	}
}

func TestUpdateTaskUnknownProjectFails(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "Orphan"}); rc != 0 {
		t.Fatal("add task")
	}
	if rc := cmdUpdateTask([]string{"orphan", "--project", "nope"}); rc == 0 {
		t.Error("expected failure for unknown project")
	}
	if rc := cmdUpdateTask([]string{"orphan", "--project", "x", "--clear-project"}); rc == 0 {
		t.Error("expected failure for mutually exclusive flags")
	}
}
