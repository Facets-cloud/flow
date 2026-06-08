package app

import (
	"errors"
	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
	"strings"
	"testing"
)

// stubAutoLauncher replaces autoLauncher with a recorder that returns a
// fixed pid and never spawns a real supervisor. Returns accessors for
// the captured slug / workDir / logPath / injection and the call count.
func stubAutoLauncher(t *testing.T, pid int) (calls *int, slug, workDir, logPath, injection *string) {
	t.Helper()
	var n int
	var s, wd, lp, inj string
	old := autoLauncher
	autoLauncher = func(gotSlug, gotWorkDir, gotLog, gotInjection string, env []string) (int, error) {
		n++
		s, wd, lp, inj = gotSlug, gotWorkDir, gotLog, gotInjection
		return pid, nil
	}
	t.Cleanup(func() { autoLauncher = old })
	return &n, &s, &wd, &lp, &inj
}

// TestCmdDoAutoLaunchesDetached pins the core --auto behavior: no terminal
// tab is spawned, the detached supervisor launcher is invoked, and the
// task's auto-run bookkeeping is recorded with status 'running'.
func TestCmdDoAutoLaunchesDetached(t *testing.T) {
	root := setupFlowRoot(t)
	seedTask(t, "auto-task")

	itermCount, _ := stubITerm(t)
	calls, gotSlug, gotWorkDir, gotLog, gotInjection := stubAutoLauncher(t, 4242)

	if rc := cmdDo([]string{"auto-task", "--auto"}); rc != 0 {
		t.Fatalf("cmdDo --auto rc=%d, want 0", rc)
	}
	if *gotInjection != "" {
		t.Errorf("injection = %q, want empty (no --with passed)", *gotInjection)
	}
	if *itermCount != 0 {
		t.Errorf("iterm spawn count = %d, want 0 (--auto must not open a tab)", *itermCount)
	}
	if *calls != 1 {
		t.Fatalf("autoLauncher calls = %d, want 1", *calls)
	}
	if *gotSlug != "auto-task" {
		t.Errorf("launcher slug = %q, want auto-task", *gotSlug)
	}
	if !strings.Contains(*gotLog, "tasks/auto-task/auto-runs/") {
		t.Errorf("launcher logPath = %q, want path under tasks/auto-task/auto-runs/", *gotLog)
	}
	if !strings.HasSuffix(*gotLog, ".log") {
		t.Errorf("launcher logPath = %q, want .log suffix", *gotLog)
	}
	_ = root
	_ = gotWorkDir

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "auto-task")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "in-progress" {
		t.Errorf("status = %q, want in-progress", task.Status)
	}
	if !task.SessionID.Valid || task.SessionID.String == "" {
		t.Error("session_id should be pre-allocated by --auto bootstrap")
	}
	if task.AutoRunStatus.String != "running" {
		t.Errorf("auto_run_status = %q, want running", task.AutoRunStatus.String)
	}
	if !task.AutoRunPID.Valid || task.AutoRunPID.Int64 != 4242 {
		t.Errorf("auto_run_pid = %+v, want 4242", task.AutoRunPID)
	}
	if !task.AutoRunStarted.Valid {
		t.Error("auto_run_started should be set")
	}
	if task.AutoRunFinished.Valid {
		t.Error("auto_run_finished should be NULL while running")
	}
	if task.AutoRunLog.String == "" {
		t.Error("auto_run_log should be recorded")
	}
}

// TestCmdDoAutoRejectsHere: --auto cannot combine with --here (--here binds
// the current session; --auto spawns its own). --with IS allowed (see
// TestCmdDoAutoWithInjection).
func TestCmdDoAutoRejectsHere(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "auto-task")
	stubITerm(t)
	stubAutoLauncher(t, 1)

	if rc := cmdDo([]string{"auto-task", "--auto", "--here"}); rc != 2 {
		t.Errorf("--auto --here rc=%d, want 2", rc)
	}
}

// TestCmdDoAutoWithInjection: --auto --with threads the instruction to the
// detached supervisor so it can append it to the autonomous prompt.
func TestCmdDoAutoWithInjection(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "auto-task")
	stubITerm(t)
	calls, _, _, _, gotInjection := stubAutoLauncher(t, 4242)

	if rc := cmdDo([]string{"auto-task", "--auto", "--with", "also check the rate limiter"}); rc != 0 {
		t.Fatalf("--auto --with rc=%d, want 0", rc)
	}
	if *calls != 1 {
		t.Fatalf("autoLauncher calls = %d, want 1", *calls)
	}
	if *gotInjection != "also check the rate limiter" {
		t.Errorf("injection = %q, want the --with text", *gotInjection)
	}
}

// TestAutoExecAppendsInjection: cmdAutoExec, given a --with instruction,
// appends the marker + instruction to the prompt it hands to claude.
func TestAutoExecAppendsInjection(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "auto-task")
	seedRunningAuto(t, "auto-task", 4242)

	var gotOpts harness.LaunchOpts
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		gotOpts = opts
		return nil
	})

	if rc := cmdAutoExec([]string{"auto-task", "--with", "extra instruction here"}); rc != 0 {
		t.Fatalf("cmdAutoExec rc=%d, want 0", rc)
	}
	// The instruction is threaded via opts.Inject; the harness's
	// AutoRunArgv wraps it behind InjectionMarker (covered in claude_test).
	if gotOpts.Inject != "extra instruction here" {
		t.Errorf("opts.Inject = %q, want the --with text", gotOpts.Inject)
	}
	if !gotOpts.SkipPermissions {
		t.Errorf("auto runs must set SkipPermissions")
	}
}

// TestCmdDoAutoRefusesWhenAlreadyRunning: a second --auto launch while a
// prior run is still 'running' (live pid) is refused without --force.
func TestCmdDoAutoRefusesWhenAlreadyRunning(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "auto-task")
	stubITerm(t)
	stubAutoLauncher(t, 4242)

	// Mark the pid alive so the running-guard fires.
	oldAlive := processAlive
	processAlive = func(pid int) bool { return true }
	t.Cleanup(func() { processAlive = oldAlive })

	if rc := cmdDo([]string{"auto-task", "--auto"}); rc != 0 {
		t.Fatalf("first --auto rc=%d, want 0", rc)
	}
	if rc := cmdDo([]string{"auto-task", "--auto"}); rc != 1 {
		t.Errorf("second --auto rc=%d, want 1 (already running)", rc)
	}
	if rc := cmdDo([]string{"auto-task", "--auto", "--force"}); rc != 0 {
		t.Errorf("--auto --force rc=%d, want 0 (override running guard)", rc)
	}
}

// stubAutoRunner replaces autoRunner with fn.
func stubAutoRunner(t *testing.T, fn func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error) {
	t.Helper()
	old := autoRunner
	autoRunner = fn
	t.Cleanup(func() { autoRunner = old })
}

// seedRunningAuto creates an in-progress task already in auto 'running'
// state, as if a supervisor had just launched it.
func seedRunningAuto(t *testing.T, slug string, pid int) {
	t.Helper()
	db := openFlowDB(t)
	now := flowdb.NowISO()
	if _, err := db.Exec(
		`UPDATE tasks SET status='in-progress', session_id=?, session_started=?,
		 auto_run_status='running', auto_run_pid=?, auto_run_started=?, updated_at=?
		 WHERE slug=?`,
		fakeSessionID(slug), now, pid, now, now, slug,
	); err != nil {
		t.Fatal(err)
	}
}

// TestAutoExecFinalizesCompleted: when the headless session self-completes
// (task ends up 'done' and the runner returns nil), __auto-exec records
// 'completed' and clears the pid.
func TestAutoExecFinalizesCompleted(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "auto-task")
	seedRunningAuto(t, "auto-task", 4242)

	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		// Simulate the session calling `flow done` on itself.
		db := openFlowDB(t)
		if _, err := db.Exec(`UPDATE tasks SET status='done' WHERE slug='auto-task'`); err != nil {
			return err
		}
		return nil
	})

	if rc := cmdAutoExec([]string{"auto-task"}); rc != 0 {
		t.Fatalf("cmdAutoExec rc=%d, want 0", rc)
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "auto-task")
	if err != nil {
		t.Fatal(err)
	}
	if task.AutoRunStatus.String != "completed" {
		t.Errorf("auto_run_status = %q, want completed", task.AutoRunStatus.String)
	}
	if !task.AutoRunFinished.Valid {
		t.Error("auto_run_finished should be set after finalize")
	}
	if task.AutoRunPID.Valid {
		t.Errorf("auto_run_pid should be cleared after finalize; got %d", task.AutoRunPID.Int64)
	}
}

// TestAutoExecFinalizesDead: a runner error (or a session that never marks
// itself done) finalizes as 'dead'.
func TestAutoExecFinalizesDead(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "auto-task")
	seedRunningAuto(t, "auto-task", 4242)

	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		return errors.New("harness exploded")
	})

	if rc := cmdAutoExec([]string{"auto-task"}); rc != 1 {
		t.Fatalf("cmdAutoExec rc=%d, want 1 on runner error", rc)
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "auto-task")
	if err != nil {
		t.Fatal(err)
	}
	if task.AutoRunStatus.String != "dead" {
		t.Errorf("auto_run_status = %q, want dead", task.AutoRunStatus.String)
	}
	if !task.AutoRunFinished.Valid {
		t.Error("auto_run_finished should be set even for dead runs")
	}
}

// TestReconcileAutoRunDeadPid: a 'running' row whose supervisor pid is no
// longer alive is reconciled to 'dead' on read.
func TestReconcileAutoRunDeadPid(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "auto-task")
	seedRunningAuto(t, "auto-task", 999999)

	oldAlive := processAlive
	processAlive = func(pid int) bool { return false } // pid dead
	t.Cleanup(func() { processAlive = oldAlive })

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "auto-task")
	if err != nil {
		t.Fatal(err)
	}
	reconcileAutoRun(db, task)
	if task.AutoRunStatus.String != "dead" {
		t.Errorf("after reconcile, status = %q, want dead", task.AutoRunStatus.String)
	}
	// Persisted, too.
	reloaded, _ := flowdb.GetTask(db, "auto-task")
	if reloaded.AutoRunStatus.String != "dead" {
		t.Errorf("reconcile should persist; status = %q, want dead", reloaded.AutoRunStatus.String)
	}
}

// TestReconcileAutoRunLivePidStaysRunning: a live pid keeps 'running'.
func TestReconcileAutoRunLivePidStaysRunning(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "auto-task")
	seedRunningAuto(t, "auto-task", 4242)

	oldAlive := processAlive
	processAlive = func(pid int) bool { return true }
	t.Cleanup(func() { processAlive = oldAlive })

	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "auto-task")
	reconcileAutoRun(db, task)
	if task.AutoRunStatus.String != "running" {
		t.Errorf("live pid should stay running; got %q", task.AutoRunStatus.String)
	}
}

// TestAutoRunnerUsesHarnessArgv verifies the supervisor builds its argv
// from the resolved harness (not a hardcoded binary). The per-harness
// argv shape is unit-tested in each adapter (claude_test.go's
// TestAutoRunArgv); here we just confirm the app layer delegates.
func TestAutoRunnerUsesHarnessArgv(t *testing.T) {
	h := claude.New()
	argv := h.AutoRunArgv("sess-123", "do the work", harness.LaunchOpts{SkipPermissions: true})
	joined := strings.Join(argv, " ")
	for _, want := range []string{"claude", "--session-id", "sess-123", "-p", "do the work", "--dangerously-skip-permissions"} {
		if !strings.Contains(joined, want) {
			t.Errorf("AutoRunArgv missing %q; got %v", want, argv)
		}
	}
}

// TestBuildAutoBootstrapPrompt sanity-checks that the autonomous prompt
// tells the session it is headless, forbids interactive prompting,
// instructs self-closure via flow done, AND insists on persisting toward
// a closeable state (giving up only as a last resort).
func TestBuildAutoBootstrapPrompt(t *testing.T) {
	p := buildAutoBootstrapPrompt("my-task", "regular", "")
	for _, want := range []string{
		"my-task", "flow done", "AskUserQuestion", "autonomous",
		"PERSIST", "LAST RESORT", "EXHAUST",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("auto prompt missing %q", want)
		}
	}
}
