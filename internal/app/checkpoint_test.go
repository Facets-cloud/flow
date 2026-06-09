package app

import (
	"errors"
	"flow/internal/flowdb"
	"testing"
)

// TestCmdCheckpointRunsSweepWhenSessionExists verifies the happy path:
// a task with a session_id gets exactly one sweep call carrying a prompt
// that loads the skill, reads the transcript, and targets the task's
// updates/ dir — and returns rc=0.
func TestCmdCheckpointRunsSweepWhenSessionExists(t *testing.T) {
	setupFlowRoot(t)
	calls := stubClaudeRunner(t, nil)
	if rc := cmdAdd([]string{"task", "Has Session"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	// Seed a session_id so the transcript gate fires.
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=? WHERE slug=?`,
		"deadbeef-uuid", flowdb.NowISO(), "has-session",
	); err != nil {
		t.Fatalf("seed session_id: %v", err)
	}
	db.Close()

	if rc := cmdCheckpoint([]string{"has-session"}); rc != 0 {
		t.Fatalf("checkpoint rc=%d, want 0", rc)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 sweep call, got %d", len(*calls))
	}
	got := (*calls)[0].prompt
	for _, want := range []string{
		"flow skill",
		"flow transcript has-session",
		"/tasks/has-session/updates/",
		"checkpoint",
	} {
		if !contains(got, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

// TestCmdCheckpointDoesNotTouchKB locks the contract that a checkpoint is
// task-local: the prompt must NOT instruct the headless session to write
// KB entries (that's `flow done`'s job at the strict bar). The only kb/
// mention allowed is the explicit "do NOT write to the knowledge base"
// guard, so we assert there is no kb-file write target.
func TestCmdCheckpointDoesNotTouchKB(t *testing.T) {
	setupFlowRoot(t)
	calls := stubClaudeRunner(t, nil)
	if rc := cmdAdd([]string{"task", "No KB"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=? WHERE slug=?`,
		"nokb-uuid", flowdb.NowISO(), "no-kb",
	); err != nil {
		t.Fatalf("seed session_id: %v", err)
	}
	db.Close()

	if rc := cmdCheckpoint([]string{"no-kb"}); rc != 0 {
		t.Fatalf("checkpoint rc=%d", rc)
	}
	got := (*calls)[0].prompt
	for _, unwanted := range []string{"kb/user.md", "kb/org.md", "kb/products.md"} {
		if contains(got, unwanted) {
			t.Errorf("checkpoint prompt unexpectedly targets a KB file: %q", unwanted)
		}
	}
}

// TestCmdCheckpointRefusesTaskWithoutSession: a backlog task with no
// session_id has no transcript to read, so checkpoint refuses (rc=1) and
// never calls the sweep.
func TestCmdCheckpointRefusesTaskWithoutSession(t *testing.T) {
	setupFlowRoot(t)
	calls := stubClaudeRunner(t, errors.New("should not be called"))
	if rc := cmdAdd([]string{"task", "No Session Task"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	if rc := cmdCheckpoint([]string{"no-session-task"}); rc != 1 {
		t.Errorf("checkpoint rc=%d, want 1 (sessionless task should be refused)", rc)
	}
	if len(*calls) != 0 {
		t.Errorf("expected 0 sweep calls, got %d", len(*calls))
	}
}

// TestCmdCheckpointUnknownRef: an explicit ref that resolves to nothing
// is a hard error.
func TestCmdCheckpointUnknownRef(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	if rc := cmdCheckpoint([]string{"nope"}); rc == 0 {
		t.Error("expected rc!=0 for unknown task")
	}
}

// TestCmdCheckpointNoBindingNoRef: no ref and no bound session (the test
// process has no $CLAUDE_CODE_SESSION_ID) → friendly rc=1, no sweep.
func TestCmdCheckpointNoBindingNoRef(t *testing.T) {
	setupFlowRoot(t)
	calls := stubClaudeRunner(t, errors.New("should not be called"))
	if rc := cmdCheckpoint(nil); rc != 1 {
		t.Errorf("checkpoint rc=%d, want 1 (no ref, unbound session)", rc)
	}
	if len(*calls) != 0 {
		t.Errorf("expected 0 sweep calls, got %d", len(*calls))
	}
}

// TestCmdCheckpointSweepFailureIsError: unlike `flow done` (which swallows
// sweep failures because the status flip is the durability boundary),
// checkpoint has no status flip to protect — a non-zero sweep exit is the
// whole operation failing, surfaced as rc=1.
func TestCmdCheckpointSweepFailureIsError(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, errors.New("exec: claude: executable file not found in $PATH"))
	if rc := cmdAdd([]string{"task", "Sweep Fail"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=? WHERE slug=?`,
		"sf-uuid", flowdb.NowISO(), "sweep-fail",
	); err != nil {
		t.Fatalf("seed session_id: %v", err)
	}
	db.Close()

	if rc := cmdCheckpoint([]string{"sweep-fail"}); rc != 1 {
		t.Errorf("checkpoint rc=%d, want 1 when the sweep fails", rc)
	}
	// Status must be untouched — checkpoint never mutates the row.
	db = openFlowDB(t)
	task, err := flowdb.GetTask(db, "sweep-fail")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "backlog" {
		t.Errorf("status = %q, want backlog unchanged (checkpoint must not mutate the row)", task.Status)
	}
}

// TestBuildCheckpointSweepPromptShape pins the key instructions so a
// future edit that drops the skill load, the transcript step, or the
// no-status-change / no-KB guards gets caught.
func TestBuildCheckpointSweepPromptShape(t *testing.T) {
	setupFlowRoot(t)
	p := buildCheckpointSweepPrompt("demo")
	for _, want := range []string{
		"flow skill",
		"flow transcript demo",
		"/tasks/demo/updates/",
		"do NOT change task status",
		"NOT a close-out",
	} {
		if !contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}
