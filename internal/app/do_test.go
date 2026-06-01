package app

import (
	"bytes"
	"errors"
	"flow/internal/flowdb"
	"flow/internal/iterm"
	"flow/internal/spawner"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// wrapperPathRE matches the wrapper-script path inside the captured
// iTerm osascript. iterm.SpawnTab writes the actual command (claude
// --session-id, env exports, etc.) to a temp file matching this name
// pattern and the osascript only types `/bin/sh '<that-path>'` into
// the new tab.
var wrapperPathRE = regexp.MustCompile(`/bin/sh '([^']+flow-iterm-[^']+\.sh)'`)

// readWrapper extracts the temp wrapper-script path from a captured
// iTerm osascript and returns the wrapper's file contents. Use this
// in tests that previously asserted against the osascript itself —
// the spawner refactor moved the actual claude/codex invocation, env
// exports, and bootstrap prompt out of the osascript and into a
// per-spawn `/tmp/flow-iterm-*.sh` wrapper. Because iterm.Runner is
// stubbed in tests, the wrapper never executes (and never
// self-deletes), so the file persists on disk and is readable here.
func readWrapper(t *testing.T, script string) string {
	t.Helper()
	m := wrapperPathRE.FindStringSubmatch(script)
	if m == nil {
		t.Fatalf("no wrapper-script reference in captured osascript:\n%s", script)
	}
	data, err := os.ReadFile(m[1])
	if err != nil {
		t.Fatalf("read wrapper %s: %v", m[1], err)
	}
	return string(data)
}

// stubITerm replaces iterm.Runner with a counter + captured-script
// recorder. Returns the counter pointer and a function that reads the
// most recent AppleScript argument passed to osascript.
//
// It also pins spawner.Override to BackendITerm so the test is not
// affected by an ambient $ZELLIJ env var (e.g. when the developer runs
// the test suite from inside a zellij session).
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

	// Pin the spawner backend so ambient env vars (e.g. ZELLIJ) don't
	// reroute SpawnTab away from iterm.Runner.
	oldOverride := spawner.Override
	spawner.Override = spawner.BackendITerm
	t.Cleanup(func() { spawner.Override = oldOverride })

	return &count, func() string {
		mu.Lock()
		defer mu.Unlock()
		return lastScript
	}
}

// seedTask creates a minimal task row (floating, workspace work_dir).
func seedTask(t *testing.T, slug string) {
	t.Helper()
	if rc := cmdAdd([]string{"task", slug, "--agent", "claude"}); rc != 0 {
		t.Fatalf("seed task rc=%d", rc)
	}
}

func TestCmdDoHelpReturnsZero(t *testing.T) {
	out := captureStdout(t, func() {
		if rc := cmdDo([]string{"--help"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "Usage of do") {
		t.Fatalf("help output missing usage:\n%s", out)
	}
}

// TestCmdDoLiveSessionGuard checks that a task whose session_id is in
// the live-claude-process set refuses to spawn (when focus can't find
// the tab) unless --force is passed. This is feature 3 of the
// bundled fields/sessions task. The focus path is short-circuited by
// stubbing iterm.PSRunner with empty output so ttyForClaudeSession
// returns "" → FocusSession returns (false, nil) → fall through to
// the original error message.
func TestCmdDoLiveSessionGuard(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "live-task")

	const pinnedSID = "abcdef12-3456-4789-8abc-def012345678"
	// Pre-bind the task to the pinned session so the live check has
	// something to match against. (Without bootstrapping via cmdDo —
	// that would also try to spawn an iTerm tab.)
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=? WHERE slug='live-task'`,
		pinnedSID, flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}

	// Make ps (the app-level psRunner) say this UUID is alive so the
	// live guard fires.
	stubPS(t, "  PID COMMAND\n12345 /bin/claude --session-id "+pinnedSID+"\n")

	// Make iterm.PSRunner (the focus-path probe) return no rows so the
	// focus attempt deterministically returns (false, nil) and we fall
	// through to the original "running elsewhere" error.
	oldFocusPS := iterm.PSRunner
	iterm.PSRunner = func() ([]byte, error) { return []byte(""), nil }
	t.Cleanup(func() { iterm.PSRunner = oldFocusPS })

	count, _ := stubITerm(t)
	if rc := cmdDo([]string{"live-task"}); rc != 1 {
		t.Errorf("cmdDo: rc=%d, want 1 when live session blocks spawn (focus miss)", rc)
	}
	if *count != 0 {
		t.Errorf("iterm spawn count = %d, want 0 (guard should block)", *count)
	}

	// --force should bypass the guard (and the focus attempt). iTerm
	// runner is still stubbed from above, so spawning will succeed.
	if rc := cmdDo([]string{"live-task", "--force"}); rc != 0 {
		t.Errorf("cmdDo --force: rc=%d, want 0 (guard bypassed)", rc)
	}
	if *count != 1 {
		t.Errorf("iterm spawn count after --force = %d, want 1", *count)
	}
}

// TestCmdDoLiveSessionFocusesExistingTab pins the new behavior: when a
// task's session is already running AND the active backend can locate
// its tab, `flow do` focuses that tab and exits 0 instead of erroring.
// No new tab is spawned.
func TestCmdDoLiveSessionFocusesExistingTab(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "open-task")

	const pinnedSID = "abcdef12-3456-4789-8abc-def012345678"
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=? WHERE slug='open-task'`,
		pinnedSID, flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}

	// App-level liveClaudeSessions sees the UUID as alive.
	stubPS(t, "  PID COMMAND\n12345 /bin/claude --session-id "+pinnedSID+"\n")

	// iterm focus path: ps yields a row with tty, then osascript
	// reports "ok" → FocusSession returns (true, nil).
	oldFocusPS := iterm.PSRunner
	iterm.PSRunner = func() ([]byte, error) {
		return []byte("  PID TTY      COMMAND\n12345 ttys012  /bin/claude --session-id " + pinnedSID + "\n"), nil
	}
	t.Cleanup(func() { iterm.PSRunner = oldFocusPS })

	oldRunnerOut := iterm.RunnerOutput
	iterm.RunnerOutput = func(args []string) ([]byte, error) { return []byte("ok\n"), nil }
	t.Cleanup(func() { iterm.RunnerOutput = oldRunnerOut })

	count, _ := stubITerm(t)
	if rc := cmdDo([]string{"open-task"}); rc != 0 {
		t.Errorf("cmdDo when focus succeeds: rc=%d, want 0", rc)
	}
	if *count != 0 {
		t.Errorf("iterm spawn count = %d, want 0 (focus should not spawn)", *count)
	}
}

// TestCmdDoLiveSessionDuplicateProcessesWarn covers the duplicate-tab
// detection path. ps reports two claude processes running the same
// session UUID; cmdDo should emit a warning to stderr (so the user
// knows the duplicate exists and that transcript writes may race),
// then proceed to focus the first match. We assert that focus still
// succeeds (rc=0, no spawn) and the duplicate count surfaces in the
// captured stderr.
func TestCmdDoLiveSessionDuplicateProcessesWarn(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "dup-task")

	const pinnedSID = "abcdef12-3456-4789-8abc-def012345678"
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=? WHERE slug='dup-task'`,
		pinnedSID, flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}

	// App-level psRunner reports TWO claude processes for the same UUID.
	stubPS(t,
		"  PID COMMAND\n"+
			"12345 /bin/claude --session-id "+pinnedSID+"\n"+
			"67890 /bin/claude --resume "+pinnedSID+"\n",
	)

	// iterm focus succeeds against the first match.
	oldFocusPS := iterm.PSRunner
	iterm.PSRunner = func() ([]byte, error) {
		return []byte(
			"  PID TTY      COMMAND\n" +
				"12345 ttys012  /bin/claude --session-id " + pinnedSID + "\n" +
				"67890 ttys013  /bin/claude --resume " + pinnedSID + "\n",
		), nil
	}
	t.Cleanup(func() { iterm.PSRunner = oldFocusPS })

	oldRunnerOut := iterm.RunnerOutput
	iterm.RunnerOutput = func(args []string) ([]byte, error) { return []byte("ok\n"), nil }
	t.Cleanup(func() { iterm.RunnerOutput = oldRunnerOut })

	stderr := captureStderr(t)
	count, _ := stubITerm(t)
	if rc := cmdDo([]string{"dup-task"}); rc != 0 {
		t.Errorf("cmdDo with duplicates: rc=%d, want 0 (focus should still succeed)", rc)
	}
	if *count != 0 {
		t.Errorf("iterm spawn count = %d, want 0 (focus should not spawn)", *count)
	}
	got := stderr()
	for _, want := range []string{"2 claude processes", pinnedSID, "may race"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning missing %q\n--- stderr ---\n%s", want, got)
		}
	}
}

// captureStderr redirects os.Stderr through an os.Pipe for the duration
// of the test and returns a closure that drains and returns whatever
// was written. The original stderr is restored on Cleanup.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = origStderr
	})
	return func() string {
		_ = w.Close()
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		_ = r.Close()
		return buf.String()
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

	script := readWrapper(t, getScript())
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

func TestCmdDoRefusesBlockedTasks(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "parent-task")
	seedTask(t, "child-task")
	seedTask(t, "waiting-task")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET parent_slug = ? WHERE slug = ?`, "parent-task", "child-task"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET waiting_on = ? WHERE slug = ?`, "external approval", "waiting-task"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	count, _ := stubITerm(t)
	for _, slug := range []string{"child-task", "waiting-task"} {
		out := captureStdout(t, func() {
			if rc := cmdDo([]string{slug}); rc != 1 {
				t.Errorf("cmdDo %s rc=%d, want 1", slug, rc)
			}
		})
		if !strings.Contains(out, "error: task") {
			t.Errorf("missing blocker error for %s: %q", slug, out)
		}
	}
	if *count != 0 {
		t.Fatalf("blocked tasks spawned %d terminals, want 0", *count)
	}
	db = openFlowDB(t)
	for _, slug := range []string{"child-task", "waiting-task"} {
		task, err := flowdb.GetTask(db, slug)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status != "backlog" || task.SessionID.Valid || task.SessionStarted.Valid {
			t.Fatalf("blocked task mutated after refused do: %+v", task)
		}
	}
}

// TestCmdDoFreshSpawnFailureRollsBackSessionID pins the rollback
// invariant: when a fresh-bootstrap spawn fails (e.g. Terminal.app
// Accessibility denied), BOTH the session_id pre-allocation AND the
// status flip must be undone so the next `flow do` retries
// bootstrap fresh. Under the session-id invariant
// (status='backlog' OR session_id IS NOT NULL), preserving status
// in-progress while dropping session_id would be illegal — full
// rollback is the only consistent recovery.
//
// Repro of the user-reported bug: spawn-failure → DB has orphan
// session_id → next flow do takes resume path → claude can't find
// the jsonl. The fix rolls everything back so the next attempt is
// indistinguishable from the first.
func TestCmdDoFreshSpawnFailureRollsBackSessionID(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "fail-task")

	const pinnedSID = "ffffffff-aaaa-bbbb-cccc-dddddddddddd"
	oldNewUUID := newUUID
	newUUID = func() (string, error) { return pinnedSID, nil }
	t.Cleanup(func() { newUUID = oldNewUUID })

	// Stub iterm.Runner to fail every call — simulates the
	// Accessibility-denied path on Terminal.app, but works equally
	// well to model any spawn failure.
	old := iterm.Runner
	iterm.Runner = func(args []string) error { return errors.New("simulated osascript failure") }
	t.Cleanup(func() { iterm.Runner = old })

	// Pin the spawner backend so ambient env vars (e.g. ZELLIJ) don't
	// reroute SpawnTab away from iterm.Runner.
	oldOverride := spawner.Override
	spawner.Override = spawner.BackendITerm
	t.Cleanup(func() { spawner.Override = oldOverride })

	if rc := cmdDo([]string{"fail-task"}); rc != 1 {
		t.Errorf("cmdDo on spawn failure: got rc=%d, want 1", rc)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "fail-task")
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionID.Valid {
		t.Errorf("session_id should be NULL after spawn failure rollback; got %q", task.SessionID.String)
	}
	if task.SessionStarted.Valid {
		t.Errorf("session_started should be NULL after spawn failure rollback; got %q", task.SessionStarted.String)
	}
	// Status is rolled back to backlog so the invariant holds and the
	// next `flow do` re-flips fresh.
	if task.Status != "backlog" {
		t.Errorf("status after spawn failure: got %q, want backlog (full rollback)", task.Status)
	}
}

// TestCmdDoResumeSpawnFailureKeepsSessionID is the inverse of the
// fresh-bootstrap case: when a RESUME spawn fails (the session_id
// already pointed at a real jsonl from a previous successful spawn),
// the DB row must be left untouched. A transient osascript failure
// should not cost the user their conversation history.
func TestCmdDoResumeSpawnFailureKeepsSessionID(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "resume-fail-task")

	db := openFlowDB(t)
	const existingSID = "real-existing-sid"
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=? WHERE slug='resume-fail-task'`,
		existingSID, flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}
	db.Close()

	old := iterm.Runner
	iterm.Runner = func(args []string) error { return errors.New("simulated osascript failure") }
	t.Cleanup(func() { iterm.Runner = old })

	// Pin the spawner backend so ambient env vars (e.g. ZELLIJ) don't
	// reroute SpawnTab away from iterm.Runner.
	oldOverride := spawner.Override
	spawner.Override = spawner.BackendITerm
	t.Cleanup(func() { spawner.Override = oldOverride })

	if rc := cmdDo([]string{"resume-fail-task"}); rc != 1 {
		t.Errorf("cmdDo on resume spawn failure: got rc=%d, want 1", rc)
	}

	db = openFlowDB(t)
	task, err := flowdb.GetTask(db, "resume-fail-task")
	if err != nil {
		t.Fatal(err)
	}
	if !task.SessionID.Valid || task.SessionID.String != existingSID {
		t.Errorf("session_id was rolled back on a RESUME spawn failure; got %+v, want %s",
			task.SessionID, existingSID)
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
	script := readWrapper(t, getScript())
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
	script := readWrapper(t, getScript())
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

	// Done implies a session_id (invariant). Pre-seed one before flipping.
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET status='done', session_id=?, session_started=?, updated_at=? WHERE slug='closed-task'`,
		fakeSessionID("closed-task"), flowdb.NowISO(), flowdb.NowISO(),
	); err != nil {
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

// TestCmdDoSpawnsClaudeNotFlowde pins the post-flowde contract: `flow do`
// shells out to `claude` directly (no wrapper) for both the fresh
// bootstrap and the resume paths. Skill freshness is now an explicit
// `flow skill update` step, not an implicit per-launch refresh.
func TestCmdDoSpawnsClaudeNotFlowde(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "wrap-fresh")

	_, getScript := stubITerm(t)
	if rc := cmdDo([]string{"wrap-fresh"}); rc != 0 {
		t.Fatalf("fresh rc=%d", rc)
	}
	script := readWrapper(t, getScript())
	if !strings.Contains(script, " claude --session-id ") {
		t.Errorf("fresh spawn must invoke claude --session-id, got:\n%s", script)
	}
	// Guard against accidental reintroduction of the flowde wrapper.
	if strings.Contains(script, "flowde") {
		t.Errorf("fresh spawn should not invoke flowde, got:\n%s", script)
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
	script = readWrapper(t, getScript())
	if !strings.Contains(script, " claude --resume resume-sid") {
		t.Errorf("resume spawn must invoke claude --resume <uuid>, got:\n%s", script)
	}
	if strings.Contains(script, "flowde") {
		t.Errorf("resume spawn should not invoke flowde, got:\n%s", script)
	}
}

func TestCmdDoCodexFreshUsesInteractiveWrapper(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "codex-fresh")

	_, getScript := stubITerm(t)
	if rc := cmdDo([]string{"codex-fresh", "--agent", "codex"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "codex-fresh")
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionProvider != "codex" {
		t.Fatalf("session provider = %q, want codex", task.SessionProvider)
	}
	if task.SessionID.Valid {
		t.Fatalf("fresh codex launch should wait for capture, got session_id=%q", task.SessionID.String)
	}
	if task.Status != "in-progress" || !task.SessionStarted.Valid {
		t.Fatalf("task after codex launch = %+v", task)
	}

	script := readWrapper(t, getScript())
	for _, want := range []string{"hook codex-run", "--task", "codex-fresh", "--mode", "fresh", "--permission-mode", "auto"} {
		if !strings.Contains(script, want) {
			t.Fatalf("codex spawn script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, " claude ") {
		t.Fatalf("codex spawn script should not invoke claude:\n%s", script)
	}
}

func TestCodexCLIArgsUseInteractiveCodex(t *testing.T) {
	fresh, err := codexCLIArgs(codexModeFresh, "", "bootstrap", "/tmp/work", "/tmp/flow-root", "bypass")
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) == 0 || fresh[0] == "exec" || testContainsString(fresh, "exec") {
		t.Fatalf("fresh codex args should use interactive root command, got %#v", fresh)
	}
	for _, want := range []string{"--no-alt-screen", "-C", "/tmp/work", "--add-dir", "/tmp/flow-root", "--dangerously-bypass-approvals-and-sandbox", "bootstrap"} {
		if !testContainsString(fresh, want) {
			t.Fatalf("fresh args missing %q: %#v", want, fresh)
		}
	}

	resume, err := codexCLIArgs(codexModeResume, "abc-session", "", "/tmp/work", "/tmp/flow-root", "auto")
	if err != nil {
		t.Fatal(err)
	}
	wantResume := []string{"resume", "--include-non-interactive", "--no-alt-screen", "-C", "/tmp/work", "--add-dir", "/tmp/flow-root", "--ask-for-approval", "never", "--sandbox", "workspace-write", "abc-session"}
	if strings.Join(resume, "\x00") != strings.Join(wantResume, "\x00") {
		t.Fatalf("resume args = %#v, want %#v", resume, wantResume)
	}

	defaultFresh, err := codexCLIArgs(codexModeFresh, "", "bootstrap", "/tmp/work", "/tmp/flow-root", "default")
	if err != nil {
		t.Fatal(err)
	}
	wantDefault := []string{"--add-dir", "/tmp/flow-root", "--ask-for-approval", "on-request", "--sandbox", "workspace-write"}
	for _, want := range wantDefault {
		if !testContainsString(defaultFresh, want) {
			t.Fatalf("default fresh args missing %q: %#v", want, defaultFresh)
		}
	}
}

func testContainsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
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

func TestBuildBootstrapPromptMentionsOther(t *testing.T) {
	got := buildBootstrapPrompt("foo")
	if !strings.Contains(got, "other:") {
		t.Errorf("expected prompt to mention other:, got:\n%s", got)
	}
	if !strings.Contains(got, "load on demand") {
		t.Errorf("expected prompt to clarify lazy loading, got:\n%s", got)
	}
}

func TestBuildBootstrapPromptForPlaybookRun(t *testing.T) {
	got := buildBootstrapPromptForKind("p--2026-04-30-10-30", "playbook_run", "p")
	if !strings.Contains(got, "playbook `p`") {
		t.Errorf("expected playbook reference, got:\n%s", got)
	}
	if !strings.Contains(got, "flow show playbook p") {
		t.Errorf("expected flow show playbook command, got:\n%s", got)
	}
	if !strings.Contains(got, "snapshotted from the playbook") {
		t.Errorf("expected snapshot framing, got:\n%s", got)
	}
	if !strings.Contains(got, "other:") {
		t.Errorf("expected mention of other:, got:\n%s", got)
	}
}

func TestBuildBootstrapPromptForRegularTask(t *testing.T) {
	got := buildBootstrapPromptForKind("foo", "regular", "")
	if strings.Contains(got, "playbook") {
		t.Errorf("regular task prompt shouldn't mention playbook:\n%s", got)
	}
	if !strings.Contains(got, "flow show task") {
		t.Errorf("regular task prompt should mention flow show task:\n%s", got)
	}
}

func TestBuildBootstrapPromptForKindWithEmptyKind(t *testing.T) {
	// Defensive: an empty kind string (legacy rows that somehow didn't
	// migrate) should fall through to the regular-task variant.
	got := buildBootstrapPromptForKind("foo", "", "")
	if strings.Contains(got, "playbook") {
		t.Errorf("empty kind should default to regular, got:\n%s", got)
	}
}

func TestBuildPlaybookRunBootstrapPromptFirstRun(t *testing.T) {
	got := buildPlaybookRunBootstrapPrompt("p--2026-04-30-10-30", "p", true)
	for _, want := range []string{
		"FIRST RUN OF THIS PLAYBOOK",
		"crystallizes",
		"Add to playbook brief",
		"Save as sidecar file",
		"Capture anything from this run back to the playbook before closing",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("first-run prompt missing %q; got:\n%s", want, got)
		}
	}
}

func TestBuildPlaybookRunBootstrapPromptNotFirstRun(t *testing.T) {
	got := buildPlaybookRunBootstrapPrompt("p--2026-04-30-10-30", "p", false)
	if strings.Contains(got, "FIRST RUN OF THIS PLAYBOOK") {
		t.Errorf("non-first-run prompt should NOT have first-run banner; got:\n%s", got)
	}
	// Still has the persist-adjustments paragraph (not first-run-specific).
	if !strings.Contains(got, "adjusts the playbook") {
		t.Errorf("base playbook prompt missing persist-adjustments para")
	}
}

func TestCmdDoSetsFirstRunBannerForFirstPlaybookRun(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "Triage", "--slug", "tri-fr", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}
	_, lastScript := stubITerm(t)
	if rc := cmdRun([]string{"playbook", "tri-fr"}); rc != 0 {
		t.Fatal()
	}
	script := readWrapper(t, lastScript())
	if !strings.Contains(script, "FIRST RUN OF THIS PLAYBOOK") {
		t.Errorf("expected first-run banner in spawn script, got:\n%s", script)
	}
}

func TestCmdDoOmitsFirstRunBannerForSecondPlaybookRun(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "Triage 2", "--slug", "tri-2", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}
	_, lastScript := stubITerm(t)
	// First run.
	if rc := cmdRun([]string{"playbook", "tri-2"}); rc != 0 {
		t.Fatal()
	}
	if !strings.Contains(readWrapper(t, lastScript()), "FIRST RUN OF THIS PLAYBOOK") {
		t.Fatal("expected first-run banner on first invocation")
	}
	// Second run.
	if rc := cmdRun([]string{"playbook", "tri-2"}); rc != 0 {
		t.Fatal()
	}
	secondWrapper := readWrapper(t, lastScript())
	if strings.Contains(secondWrapper, "FIRST RUN OF THIS PLAYBOOK") {
		t.Errorf("second run should NOT have first-run banner; got:\n%s", secondWrapper)
	}
}

func TestCmdDoEmitsPlaybookVariantForPlaybookRun(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "Triage", "--slug", "tri", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}

	_, lastScript := stubITerm(t)

	// Use cmdRun to create the run-task (it uses cmdDo internally).
	if rc := cmdRun([]string{"playbook", "tri"}); rc != 0 {
		t.Fatal()
	}

	script := readWrapper(t, lastScript())
	if !strings.Contains(script, "playbook `tri`") {
		t.Errorf("expected playbook prompt variant in spawn script, got:\n%s", script)
	}
	if !strings.Contains(script, "flow show playbook tri") {
		t.Errorf("expected 'flow show playbook tri' in spawn script, got:\n%s", script)
	}
}

// TestCmdDoPropagatesFlowRootEnv pins that a custom $FLOW_ROOT in the
// parent process is forwarded to the spawned tab's command line, so
// the in-tab session reads the same DB / KB / briefs as the spawning
// process. Without this, a user with FLOW_ROOT=/elsewhere would see
// the parent process write to /elsewhere but the spawned tab fall
// back to ~/.flow.
func TestCmdDoPropagatesFlowRootEnv(t *testing.T) {
	root := setupFlowRoot(t)
	seedTask(t, "env-prop")
	t.Setenv("FLOW_ROOT", root)
	_, getScript := stubITerm(t)

	if rc := cmdDo([]string{"env-prop"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	script := readWrapper(t, getScript())
	if !strings.Contains(script, "FLOW_ROOT=") {
		t.Errorf("spawn script missing FLOW_ROOT propagation; got:\n%s", script)
	}
	if !strings.Contains(script, root) {
		t.Errorf("spawn script missing FLOW_ROOT value %q; got:\n%s", root, script)
	}
	if !strings.Contains(script, "PATH=") {
		t.Errorf("spawn script missing PATH propagation for flow binary preference; got:\n%s", script)
	}
}

// ---------- flow do --here ----------

// TestCmdDoHereHappyPath pins the in-session bind contract: with
// $CLAUDE_CODE_SESSION_ID set and a backlog target task, --here
// flips status to in-progress and writes the env's session UUID to
// tasks.session_id without spawning anything.
func TestCmdDoHereHappyPath(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "here-task")
	const sid = "f00ba111-2222-4333-8444-555555555555"
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)

	// Spawn must NOT happen — assert via stub (zero spawns).
	count, _ := stubITerm(t)
	if rc := cmdDo([]string{"here-task", "--here"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if *count != 0 {
		t.Errorf("--here should not spawn; got %d spawns", *count)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "here-task")
	if err != nil {
		t.Fatal(err)
	}
	if !task.SessionID.Valid || task.SessionID.String != sid {
		t.Errorf("session_id = %+v, want %s", task.SessionID, sid)
	}
	if task.Status != "in-progress" {
		t.Errorf("status = %q, want in-progress", task.Status)
	}
}

// TestCmdDoHereCodexHappyPath pins the Codex equivalent of the
// in-session bind contract: inside Codex, $CODEX_THREAD_ID identifies
// the current transcript. --here should bind that thread id to the
// task as a Codex session without spawning a terminal.
func TestCmdDoHereCodexHappyPath(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "codex-here-task")
	const sid = "019e3c18-1149-7532-a1c0-31a4cfedb296"
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", sid)

	count, _ := stubITerm(t)
	if rc := cmdDo([]string{"codex-here-task", "--here"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if *count != 0 {
		t.Errorf("--here should not spawn; got %d spawns", *count)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "codex-here-task")
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionProvider != sessionProviderCodex {
		t.Errorf("session_provider = %q, want codex", task.SessionProvider)
	}
	if !task.SessionID.Valid || task.SessionID.String != sid {
		t.Errorf("session_id = %+v, want %s", task.SessionID, sid)
	}
	if task.Status != "in-progress" {
		t.Errorf("status = %q, want in-progress", task.Status)
	}
}

func TestCmdDoHereCodexExplicitAgentHappyPath(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "codex-explicit-task")
	const sid = "019e3c18-1149-7532-a1c0-31a4cfedb296"
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", sid)

	if rc := cmdDo([]string{"codex-explicit-task", "--here", "--agent", "codex"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "codex-explicit-task")
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionProvider != sessionProviderCodex || !task.SessionID.Valid || task.SessionID.String != sid {
		t.Fatalf("task binding = provider %q session %+v, want codex %s", task.SessionProvider, task.SessionID, sid)
	}
}

// TestCmdDoHereNoEnvVar pins that --here errors when no Claude Code
// session is in the env (no session UUID to bind).
func TestCmdDoHereNoEnvVar(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "no-env-task")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	if rc := cmdDo([]string{"no-env-task", "--here"}); rc != 1 {
		t.Errorf("rc=%d, want 1 when CLAUDE_CODE_SESSION_ID unset", rc)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "no-env-task")
	if task.SessionID.Valid {
		t.Errorf("session_id should be NULL after refused --here, got %q", task.SessionID.String)
	}
}

// TestCmdDoHereRejectsAlreadyBound pins the wrong-session-id guard:
// if the target task already has a session_id (even one different
// from the current $CLAUDE_CODE_SESSION_ID), --here refuses without
// --force. This prevents silent overwrite of a prior binding.
func TestCmdDoHereRejectsAlreadyBound(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bound-task")

	const oldSID = "deadbeef-1111-4222-8333-444455556666"
	const newSID = "f00ba111-2222-4333-8444-555555555555"
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=?, status='in-progress' WHERE slug='bound-task'`,
		oldSID, flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}
	db.Close()

	t.Setenv("CLAUDE_CODE_SESSION_ID", newSID)
	if rc := cmdDo([]string{"bound-task", "--here"}); rc != 1 {
		t.Errorf("rc=%d, want 1 when target already bound to a different session", rc)
	}

	db = openFlowDB(t)
	task, _ := flowdb.GetTask(db, "bound-task")
	if task.SessionID.String != oldSID {
		t.Errorf("session_id changed without --force: got %q, want %s", task.SessionID.String, oldSID)
	}
}

// TestCmdDoHereForceOverwritesBinding pins that --force allows the
// overwrite of a prior binding. The user has been told this orphans
// the prior session.
func TestCmdDoHereForceOverwritesBinding(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "force-task")

	const oldSID = "deadbeef-1111-4222-8333-444455556666"
	const newSID = "f00ba111-2222-4333-8444-555555555555"
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=?, status='in-progress' WHERE slug='force-task'`,
		oldSID, flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}
	db.Close()

	t.Setenv("CLAUDE_CODE_SESSION_ID", newSID)
	if rc := cmdDo([]string{"force-task", "--here", "--force"}); rc != 0 {
		t.Errorf("rc=%d, want 0 with --force", rc)
	}
	db = openFlowDB(t)
	task, _ := flowdb.GetTask(db, "force-task")
	if task.SessionID.String != newSID {
		t.Errorf("session_id after --force: got %q, want %s", task.SessionID.String, newSID)
	}
}

// TestCmdDoHereIdempotent pins that re-running --here against a
// task already bound to THIS session is a no-op success (no error,
// no overwrite needed).
func TestCmdDoHereIdempotent(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "idem-task")
	const sid = "f00ba111-2222-4333-8444-555555555555"
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)

	if rc := cmdDo([]string{"idem-task", "--here"}); rc != 0 {
		t.Fatalf("first --here rc=%d", rc)
	}
	if rc := cmdDo([]string{"idem-task", "--here"}); rc != 0 {
		t.Errorf("second --here rc=%d, want 0 (idempotent)", rc)
	}
}

// TestCmdDoHereRejectsCurrentSessionAlreadyBoundElsewhere pins the
// no-duplicate-session_id invariant: if THIS session is already
// bound to another task, --here refuses with a friendly error
// regardless of --force. The session-id uniqueness is structural;
// no escape hatch.
func TestCmdDoHereRejectsCurrentSessionAlreadyBoundElsewhere(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "owner-task")
	seedTask(t, "intruder-task")

	const sid = "f00ba111-2222-4333-8444-555555555555"
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=?, status='in-progress' WHERE slug='owner-task'`,
		sid, flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}
	db.Close()
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)

	// Without --force.
	if rc := cmdDo([]string{"intruder-task", "--here"}); rc != 1 {
		t.Errorf("rc=%d, want 1 when current session bound elsewhere", rc)
	}
	// Even with --force the structural invariant should hold.
	if rc := cmdDo([]string{"intruder-task", "--here", "--force"}); rc != 1 {
		t.Errorf("--force rc=%d, want 1 (no override of duplicate-session check)", rc)
	}

	// owner-task still owns the session.
	db = openFlowDB(t)
	owner, _ := flowdb.GetTask(db, "owner-task")
	if owner.SessionID.String != sid {
		t.Errorf("owner-task session_id changed: got %q, want %s", owner.SessionID.String, sid)
	}
	intruder, _ := flowdb.GetTask(db, "intruder-task")
	if intruder.SessionID.Valid {
		t.Errorf("intruder-task should remain unbound; got %q", intruder.SessionID.String)
	}
}

// TestCmdDoHereRejectsDoneTask pins that --here on a done task
// refuses with a friendly pointer at the reopen path. Auto-reopen
// would silently bypass the user's previous closure intent.
func TestCmdDoHereRejectsDoneTask(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "done-task")
	// Close the task with a session_id (invariant-respecting).
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET status='done', session_id=?, session_started=? WHERE slug='done-task'`,
		fakeSessionID("done-task"), flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}
	db.Close()

	const sid = "f00ba111-2222-4333-8444-555555555555"
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)
	if rc := cmdDo([]string{"done-task", "--here"}); rc != 1 {
		t.Errorf("rc=%d, want 1 (--here on done task should refuse)", rc)
	}
}

func TestCmdDoHereRefusesBlockedTask(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "blocked-here")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET waiting_on = ? WHERE slug = ?`, "external approval", "blocked-here"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "12345678-1234-4234-8234-123456789abc")

	out := captureStdout(t, func() {
		if rc := cmdDo([]string{"blocked-here", "--here"}); rc != 1 {
			t.Fatalf("rc=%d, want 1", rc)
		}
	})
	if !strings.Contains(out, "waiting on external approval") {
		t.Fatalf("missing blocker error: %q", out)
	}
	task, err := flowdb.GetTask(db, "blocked-here")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "backlog" || task.SessionID.Valid || task.SessionStarted.Valid {
		t.Fatalf("blocked --here should not bind session: %+v", task)
	}
}

// TestCmdDoRedirectsProjectAttachedWorkspaceTaskToRepo pins the cmdDo
// self-heal for the project-workdir-bug: a project-attached task that is
// still sitting in a throwaway workspace (e.g. one created before the fix,
// or a Slack/GitHub task attached after the fact) is redirected to the
// project's real repo at open time, and the worktree is created there —
// not inside the clone.
func TestCmdDoRedirectsProjectAttachedWorkspaceTaskToRepo(t *testing.T) {
	root := setupFlowRoot(t)
	stubITerm(t)
	repo := initGitRepoForWorktreeTest(t)

	if rc := cmdAdd([]string{"project", "Demo", "--slug", "demo", "--work-dir", repo}); rc != 0 {
		t.Fatalf("add project rc=%d", rc)
	}
	// Floating task → auto-workspace work_dir.
	seedTask(t, "stranded")
	db := openFlowDB(t)
	// Construct the bug state directly: project-attached but still pointing at
	// the throwaway workspace. We bypass cmdUpdate (whose adoption logic would
	// already fix this) so the test exercises cmdDo's lazy self-heal path for
	// rows that slipped through before the fix landed.
	if _, err := db.Exec(`UPDATE tasks SET project_slug='demo' WHERE slug='stranded'`); err != nil {
		t.Fatalf("seed bug state: %v", err)
	}
	wantWorkspace := filepath.Join(root, "tasks", "stranded", "workspace")
	if before, _ := flowdb.GetTask(db, "stranded"); before.WorkDir != wantWorkspace {
		t.Fatalf("precondition: work_dir = %q, want auto-workspace %q", before.WorkDir, wantWorkspace)
	}

	if rc := cmdDo([]string{"stranded"}); rc != 0 {
		t.Fatalf("do rc=%d", rc)
	}

	proj, _ := flowdb.GetProject(db, "demo")
	after, _ := flowdb.GetTask(db, "stranded")
	if after.WorkDir != proj.WorkDir {
		t.Errorf("work_dir = %q, want redirected to project repo %q", after.WorkDir, proj.WorkDir)
	}
	wantWT := filepath.Join(repo, ".claude", "worktrees", "stranded")
	if _, err := os.Stat(wantWT); err != nil {
		t.Errorf("worktree dir missing after redirect: %v", err)
	}
}
