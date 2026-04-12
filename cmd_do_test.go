package main

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// stubITerm replaces osascriptRunner with a counter + captured-script
// recorder. Returns the counter pointer and a function that reads the
// most recent AppleScript argument passed to osascript.
func stubITerm(t *testing.T) (*int64, func() string) {
	t.Helper()
	var count int64
	var mu sync.Mutex
	var lastScript string
	old := osascriptRunner
	osascriptRunner = func(args []string) error {
		atomic.AddInt64(&count, 1)
		mu.Lock()
		if len(args) >= 2 {
			lastScript = args[1]
		}
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { osascriptRunner = old })
	return &count, func() string {
		mu.Lock()
		defer mu.Unlock()
		return lastScript
	}
}

// seedTask creates a minimal task row (floating, workspace work_dir).
func seedTask(t *testing.T, slug string) {
	t.Helper()
	if rc := cmdAdd([]string{"task", slug}); rc != 0 {
		t.Fatalf("seed task rc=%d", rc)
	}
}

// TestCmdDoFreshSpawnsClaudeWithPrompt verifies the new contract-based
// bootstrap: a fresh task spawns `claude "<prompt>"` with no --session-id
// and no --resume, and leaves session_id NULL in the DB (the execution
// session is expected to call `flow register-session` to fill it in).
func TestCmdDoFreshSpawnsClaudeWithPrompt(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "fresh-task")
	_, getScript := stubITerm(t)

	if rc := cmdDo([]string{"fresh-task"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}

	db := openFlowDB(t)
	task, err := GetTask(db, "fresh-task")
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionID.Valid {
		t.Errorf("session_id should stay NULL on fresh spawn (got %q)", task.SessionID.String)
	}
	if task.Status != "in-progress" {
		t.Errorf("status: got %q, want in-progress", task.Status)
	}

	script := getScript()
	if strings.Contains(script, "--resume") {
		t.Errorf("fresh spawn should not use --resume: %s", script)
	}
	if strings.Contains(script, "--session-id") {
		t.Errorf("fresh spawn should not use --session-id: %s", script)
	}
	// The positional prompt should reference the task slug and the
	// register-session instruction.
	if !strings.Contains(script, "fresh-task") {
		t.Errorf("spawn script missing task slug: %s", script)
	}
	if !strings.Contains(script, "register-session") {
		t.Errorf("spawn script missing register-session instruction: %s", script)
	}
}

func TestCmdDoResumesExistingSession(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "old-task")

	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET session_id='existing-sid', session_started=? WHERE slug='old-task'`, nowISO()); err != nil {
		t.Fatal(err)
	}
	db.Close()

	_, getScript := stubITerm(t)
	if rc := cmdDo([]string{"old-task"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db = openFlowDB(t)
	task, _ := GetTask(db, "old-task")
	if task.SessionID.String != "existing-sid" {
		t.Errorf("session_id got %q, want existing-sid", task.SessionID.String)
	}
	if !task.SessionLastResumed.Valid {
		t.Error("session_last_resumed should be set on resume")
	}
	script := getScript()
	if !strings.Contains(script, "--resume existing-sid") {
		t.Errorf("resume spawn should use --resume: %s", script)
	}
}

func TestCmdDoFreshClearsStaleSession(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "stale-task")

	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET session_id='stale-uuid', session_started=? WHERE slug='stale-task'`, nowISO()); err != nil {
		t.Fatal(err)
	}
	db.Close()

	_, getScript := stubITerm(t)
	if rc := cmdDo([]string{"stale-task", "--fresh"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db = openFlowDB(t)
	task, _ := GetTask(db, "stale-task")
	if task.SessionID.Valid {
		t.Errorf("session_id should be NULL after --fresh (got %q)", task.SessionID.String)
	}
	script := getScript()
	if strings.Contains(script, "--resume") {
		t.Errorf("--fresh should not spawn --resume: %s", script)
	}
}

func TestCmdDoDoneTaskRefused(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "closed-task")

	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET status='done', updated_at=? WHERE slug='closed-task'`, nowISO()); err != nil {
		t.Fatal(err)
	}
	db.Close()

	spawns, _ := stubITerm(t)
	if rc := cmdDo([]string{"closed-task"}); rc != 1 {
		t.Errorf("rc=%d, want 1 for done task", rc)
	}
	if atomic.LoadInt64(spawns) != 0 {
		t.Errorf("done task should not spawn iTerm: got %d spawns", *spawns)
	}
}

func TestCmdDoFuzzyAmbiguous(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "auth fix")
	seedTask(t, "auth refactor")

	spawns, _ := stubITerm(t)
	if rc := cmdDo([]string{"auth"}); rc != 1 {
		t.Errorf("rc=%d, want 1 for ambiguous ref", rc)
	}
	if atomic.LoadInt64(spawns) != 0 {
		t.Errorf("ambiguous ref should not spawn: %d", *spawns)
	}
}

func TestCmdDoFuzzyExactWins(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "auth")
	seedTask(t, "auth fix")

	stubITerm(t)
	if rc := cmdDo([]string{"auth"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	task, _ := GetTask(db, "auth")
	if task.Status != "in-progress" {
		t.Errorf("status=%q, want in-progress", task.Status)
	}
}

// TestCmdDoConcurrentFreshTasks verifies two concurrent cmdDo calls on a
// fresh task don't corrupt DB state. Both spawn (leave session_id NULL);
// the register-session contract handles the actual session_id write
// later when the execution sessions self-report.
func TestCmdDoConcurrentFreshTasks(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "race-task")
	spawns, _ := stubITerm(t)

	var wg sync.WaitGroup
	results := make([]int, 2)
	wg.Add(2)
	go func() { defer wg.Done(); results[0] = cmdDo([]string{"race-task"}) }()
	go func() { defer wg.Done(); results[1] = cmdDo([]string{"race-task"}) }()
	wg.Wait()

	for i, rc := range results {
		if rc != 0 {
			t.Errorf("goroutine %d rc=%d", i, rc)
		}
	}
	db := openFlowDB(t)
	task, _ := GetTask(db, "race-task")
	if task.SessionID.Valid {
		t.Errorf("session_id should be NULL after fresh races (got %q)", task.SessionID.String)
	}
	if n := atomic.LoadInt64(spawns); n != 2 {
		t.Errorf("iTerm spawn count=%d, want 2", n)
	}
}
