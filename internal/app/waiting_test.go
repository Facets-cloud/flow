package app

import (
	"database/sql"
	"testing"
)

func TestCmdWaitingSet(t *testing.T) {
	root := setupArchiveTestEnv(t)
	db := reopenArchiveTestDB(t, root)
	insertTask(t, db, "work", "Work", "in-progress", "medium", "/tmp/wd", nil)

	if rc := cmdWaiting([]string{"work", "Anshul's review"}); rc != 0 {
		t.Fatalf("waiting rc=%d", rc)
	}
	var wo sql.NullString
	var status string
	if err := db.QueryRow("SELECT waiting_on, status FROM tasks WHERE slug='work'").Scan(&wo, &status); err != nil {
		t.Fatalf("%v", err)
	}
	if !wo.Valid || wo.String != "Anshul's review" {
		t.Errorf("waiting_on got %+v, want Anshul's review", wo)
	}
	if status != "in-progress" {
		t.Errorf("status should not change, got %q", status)
	}
}

func TestCmdWaitingClear(t *testing.T) {
	root := setupArchiveTestEnv(t)
	db := reopenArchiveTestDB(t, root)
	insertTask(t, db, "work", "Work", "in-progress", "medium", "/tmp/wd", nil)
	if _, err := db.Exec("UPDATE tasks SET waiting_on = ? WHERE slug = 'work'", "Stripe keys"); err != nil {
		t.Fatal(err)
	}
	if rc := cmdWaiting([]string{"work", "--clear"}); rc != 0 {
		t.Fatalf("waiting --clear rc=%d", rc)
	}
	var wo sql.NullString
	if err := db.QueryRow("SELECT waiting_on FROM tasks WHERE slug='work'").Scan(&wo); err != nil {
		t.Fatalf("%v", err)
	}
	if wo.Valid {
		t.Errorf("waiting_on not cleared: %+v", wo)
	}
}

func TestCmdWaitingOnProjectErrors(t *testing.T) {
	root := setupArchiveTestEnv(t)
	db := reopenArchiveTestDB(t, root)
	insertProject(t, db, "myproj", "My Proj", "/tmp/wd", "medium")
	if rc := cmdWaiting([]string{"myproj", "something"}); rc == 0 {
		t.Errorf("expected error when applying waiting to a project")
	}
}

func TestCmdWaitingUnknownSlug(t *testing.T) {
	setupArchiveTestEnv(t)
	if rc := cmdWaiting([]string{"does-not-exist", "something"}); rc == 0 {
		t.Errorf("expected error for unknown slug")
	}
}

func TestCmdWaitingClearWithTextErrors(t *testing.T) {
	root := setupArchiveTestEnv(t)
	db := reopenArchiveTestDB(t, root)
	insertTask(t, db, "work", "Work", "in-progress", "medium", "/tmp/wd", nil)
	if rc := cmdWaiting([]string{"work", "--clear", "extra"}); rc == 0 {
		t.Errorf("expected error for --clear with text")
	}
}

func TestCmdWaitingMissingArg(t *testing.T) {
	root := setupArchiveTestEnv(t)
	db := reopenArchiveTestDB(t, root)
	insertTask(t, db, "work", "Work", "in-progress", "medium", "/tmp/wd", nil)
	if rc := cmdWaiting([]string{"work"}); rc == 0 {
		t.Errorf("expected error for missing text/--clear")
	}
}
