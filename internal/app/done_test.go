package app

import (
	"errors"
	"flow/internal/flowdb"
	"testing"
)

// stubClaudeRunner replaces claudeRunner with a capturing stub that returns
// the supplied error. Returns a *call counter and a *captured-args record so
// tests can assert how the runner was invoked.
type capturedClaudeCall struct {
	slug   string
	prompt string
}

func stubClaudeRunner(t *testing.T, retErr error) *[]capturedClaudeCall {
	t.Helper()
	old := claudeRunner
	calls := &[]capturedClaudeCall{}
	claudeRunner = func(slug, prompt string) error {
		*calls = append(*calls, capturedClaudeCall{slug: slug, prompt: prompt})
		return retErr
	}
	t.Cleanup(func() { claudeRunner = old })
	return calls
}

func TestCmdDoneHappyPath(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	if rc := cmdAdd([]string{"task", "Some Task"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	// Session-id invariant: done requires a session_id. Pre-seed one
	// so the close-out is legal.
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=? WHERE slug='some-task'`,
		fakeSessionID("some-task"), flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if rc := cmdDone([]string{"some-task"}); rc != 0 {
		t.Fatalf("done rc=%d", rc)
	}
	db = openFlowDB(t)
	task, err := flowdb.GetTask(db, "some-task")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "done" {
		t.Errorf("status: got %q, want done", task.Status)
	}
}

func TestCmdDoneUnknownRef(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	if rc := cmdDone([]string{"nope"}); rc == 0 {
		t.Error("expected rc!=0 for unknown task")
	}
}

func TestCmdDoneIdempotent(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	if rc := cmdAdd([]string{"task", "Idem"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	// Pre-seed a session_id so done is legal under the invariant.
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=? WHERE slug='idem'`,
		fakeSessionID("idem"), flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if rc := cmdDone([]string{"idem"}); rc != 0 {
		t.Fatalf("first done rc=%d", rc)
	}
	// A second done should be idempotent (status already done, session_id
	// preserved → UPDATE is a no-op semantically).
	if rc := cmdDone([]string{"idem"}); rc != 0 {
		t.Errorf("second done rc=%d, want 0 (idempotent)", rc)
	}
}

func TestCmdDoneNoArgs(t *testing.T) {
	if rc := cmdDone(nil); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
}

// TestCmdDoneRefusesTaskWithoutSession pins the session-id invariant
// at the friendly-error layer for `flow done`: a backlog task with
// no session_id has no transcript to sweep, so done refuses with a
// pointer to `flow archive`. Replaces the older
// "skips sweep when no session" test — that path is no longer
// reachable under the invariant.
func TestCmdDoneRefusesTaskWithoutSession(t *testing.T) {
	setupFlowRoot(t)
	calls := stubClaudeRunner(t, errors.New("should not be called"))
	if rc := cmdAdd([]string{"task", "No Session Task"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	if rc := cmdDone([]string{"no-session-task"}); rc != 1 {
		t.Errorf("done rc=%d, want 1 (sessionless task should be refused)", rc)
	}
	if len(*calls) != 0 {
		t.Errorf("expected 0 sweep calls, got %d", len(*calls))
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "no-session-task")
	if task.Status != "backlog" {
		t.Errorf("status = %q, want backlog (refused done should not flip)", task.Status)
	}
}

// TestCmdDoneRunsSweepWhenSessionExists verifies that done invokes the
// claude runner exactly once with the task slug and a sweep prompt
// when the task has a session_id, and returns rc=0 on success.
func TestCmdDoneRunsSweepWhenSessionExists(t *testing.T) {
	setupFlowRoot(t)
	calls := stubClaudeRunner(t, nil)
	if rc := cmdAdd([]string{"task", "Has Session"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	// Manually populate session_id so the sweep gate fires.
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=? WHERE slug=?`,
		"deadbeef-uuid", flowdb.NowISO(), "has-session",
	); err != nil {
		t.Fatalf("seed session_id: %v", err)
	}
	db.Close()

	if rc := cmdDone([]string{"has-session"}); rc != 0 {
		t.Fatalf("done rc=%d, want 0", rc)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 sweep call, got %d", len(*calls))
	}
	got := (*calls)[0]
	if got.slug != "has-session" {
		t.Errorf("call slug = %q, want has-session", got.slug)
	}
	if got.prompt == "" {
		t.Error("call prompt is empty")
	}
	// Sanity-check the prompt mentions key behavior so a regression in
	// buildCloseoutSweepPrompt that drops the skill load or the
	// transcript step gets caught here.
	for _, want := range []string{"flow skill", "flow transcript has-session", "kb/"} {
		if !contains(got.prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

// TestCmdDoneCloseoutSweepIncludesProjectStep verifies that when the
// task is attached to a project, the close-out prompt includes the
// project-update step pointing at the project's updates/ directory.
func TestCmdDoneCloseoutSweepIncludesProjectStep(t *testing.T) {
	setupFlowRoot(t)
	calls := stubClaudeRunner(t, nil)

	wd := t.TempDir()
	if rc := cmdAdd([]string{"project", "Some Proj", "--slug", "sp", "--work-dir", wd}); rc != 0 {
		t.Fatalf("add project rc=%d", rc)
	}
	if rc := cmdAdd([]string{"task", "Has Proj", "--project", "sp"}); rc != 0 {
		t.Fatalf("add task rc=%d", rc)
	}

	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=? WHERE slug=?`,
		"hp-uuid", flowdb.NowISO(), "has-proj",
	); err != nil {
		t.Fatalf("seed session_id: %v", err)
	}
	db.Close()

	if rc := cmdDone([]string{"has-proj"}); rc != 0 {
		t.Fatalf("done rc=%d", rc)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 sweep call, got %d", len(*calls))
	}
	got := (*calls)[0].prompt
	for _, want := range []string{
		"Project update",
		"\"sp\"",
		// Path is templatized off flowRoot() so the test environment's
		// tempdir prefix appears here. Match the suffix only.
		"/projects/sp/updates/",
	} {
		if !contains(got, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

// TestCmdDoneCloseoutSweepSkipsProjectStepForFloating verifies that
// floating tasks (no project) get a prompt without any project-update
// instructions or path references.
func TestCmdDoneCloseoutSweepSkipsProjectStepForFloating(t *testing.T) {
	setupFlowRoot(t)
	calls := stubClaudeRunner(t, nil)

	if rc := cmdAdd([]string{"task", "Floating"}); rc != 0 {
		t.Fatalf("add task rc=%d", rc)
	}
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=? WHERE slug=?`,
		"f-uuid", flowdb.NowISO(), "floating",
	); err != nil {
		t.Fatalf("seed session_id: %v", err)
	}
	db.Close()

	if rc := cmdDone([]string{"floating"}); rc != 0 {
		t.Fatalf("done rc=%d", rc)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 sweep call, got %d", len(*calls))
	}
	got := (*calls)[0].prompt
	for _, unwanted := range []string{
		"Project update",
		"/projects/",
	} {
		if contains(got, unwanted) {
			t.Errorf("floating-task prompt unexpectedly contains %q", unwanted)
		}
	}
}

// TestCmdDoneSweepFailureStillSucceeds verifies that a non-zero exit
// from the sweep runner does NOT fail the done command — the status
// flip is the durability boundary, the sweep is best-effort.
func TestCmdDoneSweepFailureStillSucceeds(t *testing.T) {
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

	if rc := cmdDone([]string{"sweep-fail"}); rc != 0 {
		t.Errorf("done rc=%d, want 0 even when sweep fails", rc)
	}
	// Status must still be flipped despite the sweep failure.
	db = openFlowDB(t)
	task, err := flowdb.GetTask(db, "sweep-fail")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "done" {
		t.Errorf("status = %q, want done", task.Status)
	}
}

// contains is a tiny strings.Contains shim so done_test.go doesn't need
// a strings import just for this.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
