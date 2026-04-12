package app

import (
	"testing"
)

func TestCmdPriorityTask(t *testing.T) {
	root := setupArchiveTestEnv(t)
	db := reopenArchiveTestDB(t, root)
	insertTask(t, db, "work", "Work", "backlog", "medium", "/tmp/wd", nil)

	if rc := cmdPriority([]string{"work", "high"}); rc != 0 {
		t.Fatalf("priority rc=%d", rc)
	}
	var prio string
	if err := db.QueryRow("SELECT priority FROM tasks WHERE slug = 'work'").Scan(&prio); err != nil {
		t.Fatalf("%v", err)
	}
	if prio != "high" {
		t.Errorf("priority got %q, want high", prio)
	}
}

func TestCmdPriorityProject(t *testing.T) {
	root := setupArchiveTestEnv(t)
	db := reopenArchiveTestDB(t, root)
	insertProject(t, db, "proj", "Proj", "/tmp/proj", "medium")

	if rc := cmdPriority([]string{"proj", "low"}); rc != 0 {
		t.Fatalf("priority rc=%d", rc)
	}
	var prio string
	if err := db.QueryRow("SELECT priority FROM projects WHERE slug = 'proj'").Scan(&prio); err != nil {
		t.Fatalf("%v", err)
	}
	if prio != "low" {
		t.Errorf("priority got %q, want low", prio)
	}
}

func TestCmdPriorityInvalid(t *testing.T) {
	root := setupArchiveTestEnv(t)
	db := reopenArchiveTestDB(t, root)
	insertTask(t, db, "work", "Work", "backlog", "medium", "/tmp/wd", nil)
	if rc := cmdPriority([]string{"work", "urgent"}); rc == 0 {
		t.Errorf("expected error for invalid priority")
	}
}

func TestCmdPriorityUnknownRef(t *testing.T) {
	setupArchiveTestEnv(t)
	if rc := cmdPriority([]string{"nope", "high"}); rc == 0 {
		t.Errorf("expected error for unknown ref")
	}
}

func TestCmdPriorityWrongArgCount(t *testing.T) {
	setupArchiveTestEnv(t)
	if rc := cmdPriority([]string{"only-ref"}); rc == 0 {
		t.Errorf("expected error for missing priority arg")
	}
}
