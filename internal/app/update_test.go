package app

import (
	"flow/internal/flowdb"
	"path/filepath"
	"testing"
)

func TestCmdUpdateTaskSessionID(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-sid")

	const newSID = "11111111-2222-4333-8444-555555555555"
	if rc := cmdUpdate([]string{"task", "ut-sid", "--session-id", newSID}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "ut-sid")
	if task.SessionID.String != newSID {
		t.Errorf("session_id = %q, want %s", task.SessionID.String, newSID)
	}
	if !task.SessionStarted.Valid {
		t.Error("session_started should be set")
	}
}

func TestCmdUpdateTaskRejectsBadUUID(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-bad")

	if rc := cmdUpdate([]string{"task", "ut-bad", "--session-id", "not-a-uuid"}); rc != 2 {
		t.Errorf("rc=%d, want 2 for invalid uuid", rc)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "ut-bad")
	if task.SessionID.Valid {
		t.Errorf("session_id should remain NULL on invalid input, got %q", task.SessionID.String)
	}
}

func TestCmdUpdateTaskWorkDir(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-wd")

	newDir := filepath.Join(t.TempDir(), "new-spot")
	if rc := cmdUpdate([]string{"task", "ut-wd", "--work-dir", newDir, "--mkdir"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "ut-wd")
	if task.WorkDir != newDir {
		t.Errorf("work_dir = %q, want %q", task.WorkDir, newDir)
	}
}

func TestCmdUpdateTaskWorkDirMissingNoMkdir(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-nomkdir")

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if rc := cmdUpdate([]string{"task", "ut-nomkdir", "--work-dir", missing}); rc != 1 {
		t.Errorf("rc=%d, want 1 when path is missing without --mkdir", rc)
	}
}

func TestCmdUpdateTaskBothFields(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-both")

	const sid = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	newDir := filepath.Join(t.TempDir(), "combo")
	if rc := cmdUpdate([]string{"task", "ut-both",
		"--session-id", sid, "--work-dir", newDir, "--mkdir"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "ut-both")
	if task.SessionID.String != sid {
		t.Errorf("session_id = %q, want %s", task.SessionID.String, sid)
	}
	if task.WorkDir != newDir {
		t.Errorf("work_dir = %q, want %q", task.WorkDir, newDir)
	}
}

func TestCmdUpdateTaskRequiresFlag(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-noop")

	if rc := cmdUpdate([]string{"task", "ut-noop"}); rc != 2 {
		t.Errorf("rc=%d, want 2 when no field-changing flag is given", rc)
	}
}

func TestCmdUpdateTaskUnknownTask(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdUpdate([]string{"task", "nope",
		"--session-id", "11111111-2222-4333-8444-555555555555"}); rc != 1 {
		t.Errorf("rc=%d, want 1 for unknown task", rc)
	}
}

func TestCmdUpdateUnknownTarget(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdUpdate([]string{"project", "foo"}); rc != 2 {
		t.Errorf("rc=%d, want 2 for unknown update target", rc)
	}
}
