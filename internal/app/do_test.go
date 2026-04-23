package app

import (
	"flow/internal/flowdb"
	"flow/internal/iterm"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// stubITerm replaces iterm.Runner with a counter + captured-script
// recorder. Returns the counter pointer and a function that reads the
// most recent AppleScript argument passed to osascript.
func stubITerm(t *testing.T) (*int64, func() string) {
	t.Helper()
	var count int64
	var mu sync.Mutex
	var lastScript string
	old := iterm.Runner
	iterm.Runner = func(args []string) error {
		atomic.AddInt64(&count, 1)
		mu.Lock()
		if len(args) >= 2 {
			lastScript = args[1]
		}
		mu.Unlock()
		return nil
	}
	t.Cleanup(func() { iterm.Runner = old })
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

// TestCmdDoFreshAllocatesSessionID verifies the pre-allocation contract:
// a fresh task gets a UUID written to tasks.session_id and spawns
// `claude --session-id <uuid> "<prompt>"` so the jsonl file claude creates
// lands at the deterministic path keyed on that UUID.
func TestCmdDoFreshAllocatesSessionID(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "fresh-task")
	_, getScript := stubITerm(t)

	const pinnedSID = "11111111-2222-3333-4444-555555555555"
	oldNewUUID := newUUID
	newUUID = func() (string, error) { return pinnedSID, nil }
	t.Cleanup(func() { newUUID = oldNewUUID })

	if rc := cmdDo([]string{"fresh-task"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "fresh-task")
	if err != nil {
		t.Fatal(err)
	}
	if !task.SessionID.Valid || task.SessionID.String != pinnedSID {
		t.Errorf("session_id after fresh spawn: got %+v, want %s", task.SessionID, pinnedSID)
	}
	if !task.SessionStarted.Valid {
		t.Error("session_started should be set after fresh spawn")
	}
	if task.Status != "in-progress" {
		t.Errorf("status: got %q, want in-progress", task.Status)
	}

	script := getScript()
	if strings.Contains(script, "--resume") {
		t.Errorf("fresh spawn should not use --resume: %s", script)
	}
	if !strings.Contains(script, "--session-id "+pinnedSID) {
		t.Errorf("fresh spawn should pass --session-id %s: %s", pinnedSID, script)
	}
	if !strings.Contains(script, "fresh-task") {
		t.Errorf("spawn script missing task slug: %s", script)
	}
}

func TestCmdDoResumesExistingSession(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "old-task")

	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET session_id='existing-sid', session_started=? WHERE slug='old-task'`, flowdb.NowISO()); err != nil {
		t.Fatal(err)
	}
	db.Close()

	_, getScript := stubITerm(t)
	if rc := cmdDo([]string{"old-task"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db = openFlowDB(t)
	task, _ := flowdb.GetTask(db, "old-task")
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

// TestCmdDoFreshRotatesStaleSession verifies --fresh overwrites an
// existing session_id with a newly-allocated UUID and spawns with that
// UUID via --session-id (not --resume).
func TestCmdDoFreshRotatesStaleSession(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "stale-task")

	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET session_id='stale-uuid', session_started=? WHERE slug='stale-task'`, flowdb.NowISO()); err != nil {
		t.Fatal(err)
	}
	db.Close()

	const pinnedSID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	oldNewUUID := newUUID
	newUUID = func() (string, error) { return pinnedSID, nil }
	t.Cleanup(func() { newUUID = oldNewUUID })

	_, getScript := stubITerm(t)
	if rc := cmdDo([]string{"stale-task", "--fresh"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db = openFlowDB(t)
	task, _ := flowdb.GetTask(db, "stale-task")
	if task.SessionID.String != pinnedSID {
		t.Errorf("session_id after --fresh: got %q, want %s", task.SessionID.String, pinnedSID)
	}
	script := getScript()
	if strings.Contains(script, "--resume") {
		t.Errorf("--fresh should not spawn --resume: %s", script)
	}
	if !strings.Contains(script, "--session-id "+pinnedSID) {
		t.Errorf("--fresh should spawn with --session-id %s: %s", pinnedSID, script)
	}
}

func TestCmdDoDoneTaskRefused(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "closed-task")

	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET status='done', updated_at=? WHERE slug='closed-task'`, flowdb.NowISO()); err != nil {
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
	task, _ := flowdb.GetTask(db, "auth")
	if task.Status != "in-progress" {
		t.Errorf("status=%q, want in-progress", task.Status)
	}
}

// TestCmdDoSpawnsFlowdeNotClaude pins the wrapper contract: `flow do`
// shells out to `flowde` (not `claude` directly) for both the fresh
// bootstrap and the resume paths. The `flowde` wrapper owns the
// "skill is current" guarantee, so `flow do` must not bypass it.
func TestCmdDoSpawnsFlowdeNotClaude(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "wrap-fresh")

	_, getScript := stubITerm(t)
	if rc := cmdDo([]string{"wrap-fresh"}); rc != 0 {
		t.Fatalf("fresh rc=%d", rc)
	}
	script := getScript()
	if !strings.Contains(script, " flowde ") {
		t.Errorf("fresh spawn must invoke flowde, got:\n%s", script)
	}
	// Guard against accidental reintroduction of a direct `claude`
	// invocation in the spawn command portion. We look for the two
	// shapes `flow do` used to emit: `claude '<prompt>'` (fresh) and
	// `claude --resume <uuid>` (resume).
	if strings.Contains(script, " claude ") {
		t.Errorf("fresh spawn should not invoke claude directly, got:\n%s", script)
	}

	// Now the resume path.
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET session_id='resume-sid', session_started=? WHERE slug='wrap-fresh'`, flowdb.NowISO()); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if rc := cmdDo([]string{"wrap-fresh"}); rc != 0 {
		t.Fatalf("resume rc=%d", rc)
	}
	script = getScript()
	if !strings.Contains(script, " flowde --resume resume-sid") {
		t.Errorf("resume spawn must invoke flowde --resume <uuid>, got:\n%s", script)
	}
	if strings.Contains(script, " claude ") {
		t.Errorf("resume spawn should not invoke claude directly, got:\n%s", script)
	}
}

// TestCmdDoConcurrentFreshTasks verifies two concurrent cmdDo calls on a
// fresh task don't corrupt DB state. The BEGIN IMMEDIATE lock serializes
// the txs: the winner allocates a UUID and writes it; the loser sees
// session_id already set and falls through to the resume path (spawning
// `claude --resume <winner-uuid>`). Both tabs end up pointing at the same
// session — pre-existing documented race outcome, no lost UUIDs.
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
	task, _ := flowdb.GetTask(db, "race-task")
	if !task.SessionID.Valid || task.SessionID.String == "" {
		t.Errorf("session_id should be populated after races (got %+v)", task.SessionID)
	}
	if n := atomic.LoadInt64(spawns); n != 2 {
		t.Errorf("iTerm spawn count=%d, want 2", n)
	}
}
