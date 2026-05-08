package app

import (
	"flow/internal/flowdb"
	"path/filepath"
	"testing"
	"time"
)

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

func TestCmdUpdateTaskStatusRollback(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-rollback")

	// First mark it done via flow done.
	if rc := cmdDone([]string{"ut-rollback"}); rc != 0 {
		t.Fatalf("done rc=%d", rc)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "ut-rollback")
	if task.Status != "done" {
		t.Fatalf("precondition: status = %q, want done", task.Status)
	}

	// Now roll it back to in-progress via update.
	if rc := cmdUpdate([]string{"task", "ut-rollback", "--status", "in-progress"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	task, _ = flowdb.GetTask(db, "ut-rollback")
	if task.Status != "in-progress" {
		t.Errorf("status = %q, want in-progress", task.Status)
	}
	if !task.StatusChangedAt.Valid {
		t.Error("status_changed_at should be set after a real status change")
	}
}

func TestCmdUpdateTaskStatusInvalid(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-bad-status")
	if rc := cmdUpdate([]string{"task", "ut-bad-status", "--status", "blocked"}); rc != 2 {
		t.Errorf("rc=%d, want 2 for unknown status", rc)
	}
}

func TestCmdUpdateTaskAssignee(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-assign")

	if rc := cmdUpdate([]string{"task", "ut-assign", "--assignee", "alice"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "ut-assign")
	if !task.Assignee.Valid || task.Assignee.String != "alice" {
		t.Errorf("assignee = %+v, want alice", task.Assignee)
	}

	if rc := cmdUpdate([]string{"task", "ut-assign", "--clear-assignee"}); rc != 0 {
		t.Fatalf("clear rc=%d", rc)
	}
	task, _ = flowdb.GetTask(db, "ut-assign")
	if task.Assignee.Valid {
		t.Errorf("assignee should be NULL after clear, got %q", task.Assignee.String)
	}
}

func TestCmdUpdateTaskAssigneeMutuallyExclusive(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-assign-x")
	if rc := cmdUpdate([]string{"task", "ut-assign-x",
		"--assignee", "bob", "--clear-assignee"}); rc != 2 {
		t.Errorf("rc=%d, want 2 when both given", rc)
	}
}

func TestCmdUpdateTaskPriority(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-prio")

	if rc := cmdUpdate([]string{"task", "ut-prio", "--priority", "high"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "ut-prio")
	if task.Priority != "high" {
		t.Errorf("priority = %q, want high", task.Priority)
	}
}

func TestCmdUpdateTaskPriorityInvalid(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-prio-bad")
	if rc := cmdUpdate([]string{"task", "ut-prio-bad", "--priority", "urgent"}); rc != 2 {
		t.Errorf("rc=%d, want 2 for invalid priority", rc)
	}
}

func TestCmdUpdateTaskWaiting(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-wait")

	if rc := cmdUpdate([]string{"task", "ut-wait", "--waiting", "Bob"}); rc != 0 {
		t.Fatalf("set rc=%d", rc)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "ut-wait")
	if !task.WaitingOn.Valid || task.WaitingOn.String != "Bob" {
		t.Errorf("waiting_on = %+v, want Bob", task.WaitingOn)
	}

	if rc := cmdUpdate([]string{"task", "ut-wait", "--clear-waiting"}); rc != 0 {
		t.Fatalf("clear rc=%d", rc)
	}
	task, _ = flowdb.GetTask(db, "ut-wait")
	if task.WaitingOn.Valid {
		t.Errorf("waiting_on should be NULL after clear, got %q", task.WaitingOn.String)
	}
}

func TestCmdUpdateTaskWaitingMutuallyExclusive(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-wait-x")
	if rc := cmdUpdate([]string{"task", "ut-wait-x",
		"--waiting", "Carol", "--clear-waiting"}); rc != 2 {
		t.Errorf("rc=%d, want 2 when both given", rc)
	}
}

func TestCmdUpdateProjectPriority(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"project", "Some Proj", "--slug", "up-sp", "--work-dir", wd}); rc != 0 {
		t.Fatalf("seed project rc=%d", rc)
	}

	if rc := cmdUpdate([]string{"project", "up-sp", "--priority", "low"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	p, _ := flowdb.GetProject(db, "up-sp")
	if p.Priority != "low" {
		t.Errorf("project priority = %q, want low", p.Priority)
	}
}

func TestCmdUpdateProjectPriorityInvalid(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"project", "Bad", "--slug", "up-bad", "--work-dir", wd}); rc != 0 {
		t.Fatalf("seed rc=%d", rc)
	}
	if rc := cmdUpdate([]string{"project", "up-bad", "--priority", "urgent"}); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
}

func TestCmdUpdateProjectRequiresFlag(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"project", "Empty", "--slug", "up-empty", "--work-dir", wd}); rc != 0 {
		t.Fatalf("seed rc=%d", rc)
	}
	if rc := cmdUpdate([]string{"project", "up-empty"}); rc != 2 {
		t.Errorf("rc=%d, want 2 when no flag given", rc)
	}
}

func TestCmdUpdateUnknownProject(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdUpdate([]string{"project", "nope", "--priority", "high"}); rc != 1 {
		t.Errorf("rc=%d, want 1 for unknown project", rc)
	}
}

func TestCmdUpdateTaskDueDate(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-due")

	if rc := cmdUpdate([]string{"task", "ut-due", "--due-date", "2026-12-31"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "ut-due")
	if !task.DueDate.Valid || task.DueDate.String != "2026-12-31" {
		t.Errorf("due_date = %+v, want 2026-12-31", task.DueDate)
	}

	if rc := cmdUpdate([]string{"task", "ut-due", "--clear-due"}); rc != 0 {
		t.Fatalf("clear rc=%d", rc)
	}
	task, _ = flowdb.GetTask(db, "ut-due")
	if task.DueDate.Valid {
		t.Errorf("due_date should be NULL after clear, got %q", task.DueDate.String)
	}
}

func TestCmdUpdateTaskAddTags(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-tag")

	if rc := cmdUpdate([]string{"task", "ut-tag", "--tag", "Frontend", "--tag", "URGENT"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	tags, err := flowdb.GetTaskTags(db, "ut-tag")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0] != "frontend" || tags[1] != "urgent" {
		t.Errorf("got %v, want [frontend urgent]", tags)
	}
}

func TestCmdUpdateTaskTagIdempotent(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-tag-idem")

	if rc := cmdUpdate([]string{"task", "ut-tag-idem", "--tag", "alpha", "--tag", "ALPHA", "--tag", "alpha"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	tags, _ := flowdb.GetTaskTags(db, "ut-tag-idem")
	if len(tags) != 1 || tags[0] != "alpha" {
		t.Errorf("got %v, want [alpha]", tags)
	}
}

func TestCmdUpdateTaskRemoveTag(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-tag-rm")

	if rc := cmdUpdate([]string{"task", "ut-tag-rm", "--tag", "a", "--tag", "b", "--tag", "c"}); rc != 0 {
		t.Fatalf("setup rc=%d", rc)
	}
	if rc := cmdUpdate([]string{"task", "ut-tag-rm", "--remove-tag", "b"}); rc != 0 {
		t.Fatalf("remove rc=%d", rc)
	}
	db := openFlowDB(t)
	tags, _ := flowdb.GetTaskTags(db, "ut-tag-rm")
	if len(tags) != 2 || tags[0] != "a" || tags[1] != "c" {
		t.Errorf("got %v, want [a c]", tags)
	}
}

func TestCmdUpdateTaskClearTags(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-tag-clr")

	if rc := cmdUpdate([]string{"task", "ut-tag-clr", "--tag", "x", "--tag", "y"}); rc != 0 {
		t.Fatalf("setup rc=%d", rc)
	}
	if rc := cmdUpdate([]string{"task", "ut-tag-clr", "--clear-tags"}); rc != 0 {
		t.Fatalf("clear rc=%d", rc)
	}
	db := openFlowDB(t)
	tags, _ := flowdb.GetTaskTags(db, "ut-tag-clr")
	if len(tags) != 0 {
		t.Errorf("after clear got %v, want []", tags)
	}
}

func TestCmdUpdateTaskClearTagsAndRemoveTagExclusive(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-tag-x")
	if rc := cmdUpdate([]string{"task", "ut-tag-x", "--clear-tags", "--remove-tag", "foo"}); rc != 2 {
		t.Errorf("rc=%d, want 2 when both --clear-tags and --remove-tag given", rc)
	}
}

func TestCmdUpdateTaskClearAndAddTagsCombo(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-tag-combo")

	if rc := cmdUpdate([]string{"task", "ut-tag-combo", "--tag", "old1", "--tag", "old2"}); rc != 0 {
		t.Fatalf("setup rc=%d", rc)
	}
	// --clear-tags + --tag means: drop all, then add the new ones.
	if rc := cmdUpdate([]string{"task", "ut-tag-combo", "--clear-tags", "--tag", "fresh"}); rc != 0 {
		t.Fatalf("combo rc=%d", rc)
	}
	db := openFlowDB(t)
	tags, _ := flowdb.GetTaskTags(db, "ut-tag-combo")
	if len(tags) != 1 || tags[0] != "fresh" {
		t.Errorf("got %v, want [fresh]", tags)
	}
}
