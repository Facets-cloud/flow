//go:build darwin

// Update tests that need the iterm-spawning helpers from do_test.go.
// Tagged darwin because those helpers are darwin-only.
package app

import (
	"flow/internal/flowdb"
	"testing"
)

func TestCmdUpdateTaskStatusRollback(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ut-rollback")
	stubITerm(t)

	// Bootstrap via cmdDo so the task acquires a session_id and is
	// in-progress. flow done now requires a session_id under the
	// session-id invariant.
	if rc := cmdDo([]string{"ut-rollback"}); rc != 0 {
		t.Fatalf("do rc=%d", rc)
	}
	if rc := cmdDone([]string{"ut-rollback"}); rc != 0 {
		t.Fatalf("done rc=%d", rc)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "ut-rollback")
	if task.Status != "done" {
		t.Fatalf("precondition: status = %q, want done", task.Status)
	}

	// Now roll it back to in-progress via update. session_id is still
	// set (preserved across done) so the invariant holds.
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
