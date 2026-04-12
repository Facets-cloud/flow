package main

import (
	"testing"
)

func TestCmdDoneHappyPath(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "Some Task"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	if rc := cmdDone([]string{"some-task"}); rc != 0 {
		t.Fatalf("done rc=%d", rc)
	}
	db := openFlowDB(t)
	task, err := GetTask(db, "some-task")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "done" {
		t.Errorf("status: got %q, want done", task.Status)
	}
}

func TestCmdDoneUnknownRef(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdDone([]string{"nope"}); rc == 0 {
		t.Error("expected rc!=0 for unknown task")
	}
}

func TestCmdDoneIdempotent(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "Idem"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	if rc := cmdDone([]string{"idem"}); rc != 0 {
		t.Fatalf("first done rc=%d", rc)
	}
	// After it's done, findTask still resolves it (exact match returns
	// archived-aware result). A second done should either succeed (status
	// already done → UPDATE is a no-op writing same value) or be rejected
	// cleanly. Our implementation allows re-marking — it's idempotent in
	// effect.
	if rc := cmdDone([]string{"idem"}); rc != 0 {
		t.Errorf("second done rc=%d, want 0 (idempotent)", rc)
	}
}

func TestCmdDoneNoArgs(t *testing.T) {
	if rc := cmdDone(nil); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
}
