package app

import (
	"strings"
	"testing"
	"time"

	"flow/internal/flowdb"
)

// noHeadlessLaunch fails the test if the detached headless supervisor is
// started: a --tab run must never fall back to headless.
func noHeadlessLaunch(t *testing.T) {
	t.Helper()
	old := autoLauncher
	autoLauncher = func(slug, workDir, logPath, injection string, env []string) (int, error) {
		t.Errorf("headless autoLauncher invoked for %q; --tab must spawn a tab instead", slug)
		return 1, nil
	}
	t.Cleanup(func() { autoLauncher = old })
}

func getTask(t *testing.T, slug string) *flowdb.Task {
	t.Helper()
	task, err := flowdb.GetTask(openFlowDB(t), slug)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// flow do --auto --tab opens a real interactive tab (claude) running the
// autonomous prompt with skip-permissions, and records a tab-mode run.
func TestCmdDoAutoTabSpawnsTabClaude(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "tab-task")
	const sid = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	stubNewUUID(t, sid)
	count, script := stubITerm(t)
	noHeadlessLaunch(t)

	if rc := cmdDo([]string{"tab-task", "--auto", "--tab"}); rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
	if *count != 1 {
		t.Fatalf("tab spawns = %d, want 1", *count)
	}
	s := script()
	for _, want := range []string{"claude", "--session-id " + sid, "--dangerously-skip-permissions", "AUTONOMOUS", "flow done tab-task"} {
		if !strings.Contains(s, want) {
			t.Errorf("tab command missing %q; got:\n%s", want, s)
		}
	}
	if strings.Contains(s, "NO HUMAN IS WATCHING") {
		t.Errorf("tab prompt must not claim no human is watching; got:\n%s", s)
	}

	task := getTask(t, "tab-task")
	if task.AutoRunStatus.String != "running" {
		t.Errorf("auto_run_status = %q, want running", task.AutoRunStatus.String)
	}
	if task.AutoRunMode.String != autoRunModeTab {
		t.Errorf("auto_run_mode = %q, want %q", task.AutoRunMode.String, autoRunModeTab)
	}
	if task.AutoRunPID.Valid {
		t.Errorf("auto_run_pid = %d, want NULL (no supervisor for a tab run)", task.AutoRunPID.Int64)
	}
	if !task.AutoRunStarted.Valid || task.AutoRunFinished.Valid {
		t.Errorf("started=%v finished=%v, want started set and finished NULL", task.AutoRunStarted, task.AutoRunFinished)
	}
	if task.Status != "in-progress" || task.SessionID.String != sid {
		t.Errorf("status=%q sid=%q, want in-progress/%s", task.Status, task.SessionID.String, sid)
	}
}

// Codex: the tab runs `codex resume <thread>` interactively with the
// bypass flag and the autonomous prompt.
func TestCmdDoAutoTabSpawnsTabCodex(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "tab-codex")
	const tid = "019ff4b1-6162-7263-974f-f5f1866ad0fe"
	stubCodexProbe(t, tid)
	count, script := stubITerm(t)
	noHeadlessLaunch(t)

	if rc := cmdDo([]string{"tab-codex", "--auto", "--tab", "--harness", "codex"}); rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
	if *count != 1 {
		t.Fatalf("tab spawns = %d, want 1", *count)
	}
	s := script()
	for _, want := range []string{"codex", tid, "--dangerously-bypass-approvals-and-sandbox", "AUTONOMOUS"} {
		if !strings.Contains(s, want) {
			t.Errorf("codex tab command missing %q; got:\n%s", want, s)
		}
	}
	if strings.Contains(s, "codex exec") {
		t.Errorf("tab must run interactive codex, not codex exec; got:\n%s", s)
	}
	task := getTask(t, "tab-codex")
	if task.AutoRunStatus.String != "running" || task.AutoRunMode.String != autoRunModeTab {
		t.Errorf("auto_run = %q/%q, want running/tab", task.AutoRunStatus.String, task.AutoRunMode.String)
	}
}

// A task that already has a session resumes it in the tab, with the
// autonomous prompt (and any --with) delivered as the first message.
func TestCmdDoAutoTabResumeInjectsAutoPrompt(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "tab-resume")
	stubClaudeStatOK(t)
	sid := fakeSessionID("tab-resume")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET status='in-progress', session_id=?, session_started=?, harness='claude' WHERE slug='tab-resume'`,
		sid, flowdb.NowISO()); err != nil {
		t.Fatal(err)
	}
	_, script := stubITerm(t)
	noHeadlessLaunch(t)

	if rc := cmdDo([]string{"tab-resume", "--auto", "--tab", "--with", "focus on tests"}); rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
	s := script()
	for _, want := range []string{"--resume " + sid, "AUTONOMOUS", "focus on tests", "--dangerously-skip-permissions"} {
		if !strings.Contains(s, want) {
			t.Errorf("resume tab command missing %q; got:\n%s", want, s)
		}
	}
}

func TestCmdDoTabRequiresAuto(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "t")
	count, _ := stubITerm(t)
	if rc := cmdDo([]string{"t", "--tab"}); rc != 2 {
		t.Errorf("--tab without --auto rc=%d, want 2", rc)
	}
	if *count != 0 {
		t.Errorf("no tab should open on a usage error")
	}
}

// FLOW_AUTO_TAB=1 makes --auto default to a tab; --tab=false opts back
// into headless for one run.
func TestCmdDoAutoTabEnvDefault(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "env-tab")
	seedTask(t, "env-headless")
	t.Setenv(autoTabEnvVar, "1")
	count, _ := stubITerm(t)
	calls, _, _, _, _ := stubAutoLauncher(t, 777)

	if rc := cmdDo([]string{"env-tab", "--auto"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if *count != 1 || *calls != 0 {
		t.Errorf("FLOW_AUTO_TAB=1: tabs=%d headless=%d, want 1/0", *count, *calls)
	}
	if rc := cmdDo([]string{"env-headless", "--auto", "--tab=false"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if *count != 1 || *calls != 1 {
		t.Errorf("--tab=false: tabs=%d headless=%d, want 1/1", *count, *calls)
	}
	if m := getTask(t, "env-headless").AutoRunMode; m.Valid && m.String != "" {
		t.Errorf("headless run auto_run_mode = %q, want NULL", m.String)
	}
}

// Without FLOW_AUTO_TAB, plain --auto stays headless.
func TestCmdDoAutoDefaultStaysHeadless(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "plain")
	t.Setenv(autoTabEnvVar, "")
	count, _ := stubITerm(t)
	calls, _, _, _, _ := stubAutoLauncher(t, 9)
	if rc := cmdDo([]string{"plain", "--auto"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if *count != 0 || *calls != 1 {
		t.Errorf("tabs=%d headless=%d, want 0/1", *count, *calls)
	}
}

// No supported terminal: fail loudly, suggest dropping --tab, never fall
// back to headless, and roll the fresh bootstrap back.
func TestCmdDoAutoTabNoTerminalFails(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "linux-tab")
	pinNoTerminalHost(t)
	noHeadlessLaunch(t)

	var rc int
	out := captureStdout(t, func() { rc = cmdDo([]string{"linux-tab", "--auto", "--tab"}) })
	if rc != 1 {
		t.Fatalf("rc=%d, want 1; output:\n%s", rc, out)
	}
	if !strings.Contains(out, "no supported terminal") || !strings.Contains(out, "drop --tab") {
		t.Errorf("output should name the missing terminal and suggest dropping --tab; got:\n%s", out)
	}
	if strings.Contains(out, "run this in any terminal") {
		t.Errorf("a tab auto run must not be handed off as a paste-it command; got:\n%s", out)
	}
	task := getTask(t, "linux-tab")
	if task.AutoRunStatus.Valid {
		t.Errorf("auto_run_status = %q, want NULL (nothing launched)", task.AutoRunStatus.String)
	}
	if task.Status != "backlog" || task.SessionID.Valid {
		t.Errorf("status=%q sid=%v, want backlog with no session (rolled back)", task.Status, task.SessionID)
	}
}

func TestCmdDoAutoTabRefusesPrx(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "prx-tab")
	count, _ := stubITerm(t)
	noHeadlessLaunch(t)
	if rc := cmdDo([]string{"prx-tab", "--auto", "--tab", "--harness", "prx"}); rc != 2 {
		t.Errorf("prx --tab rc=%d, want 2", rc)
	}
	if *count != 0 {
		t.Errorf("no tab should open for prx")
	}
	if getTask(t, "prx-tab").Status != "backlog" {
		t.Errorf("refusal must not flip the task")
	}
}

// A live tab run blocks a second --auto launch, like a headless one.
func TestCmdDoAutoTabRunningGuard(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "guard")
	stubITerm(t)
	noHeadlessLaunch(t)
	if rc := cmdDo([]string{"guard", "--auto", "--tab"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	// Within the startup grace the run counts as running.
	if rc := cmdDo([]string{"guard", "--auto", "--tab"}); rc != 1 {
		t.Errorf("second launch rc=%d, want 1 (already running)", rc)
	}
}

// seedTabRun puts slug into a running tab-mode auto run started `ago`.
func seedTabRun(t *testing.T, slug string, ago time.Duration) {
	t.Helper()
	db := openFlowDB(t)
	started := time.Now().Add(-ago).UTC().Format(time.RFC3339)
	if _, err := db.Exec(
		`UPDATE tasks SET status='in-progress', session_id=?, session_started=?, harness='claude',
		 auto_run_status='running', auto_run_mode='tab', auto_run_pid=NULL, auto_run_started=?, auto_run_finished=NULL
		 WHERE slug=?`, fakeSessionID(slug), started, started, slug); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileTabRunLiveStaysRunning(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "live")
	seedTabRun(t, "live", time.Hour)
	stubPS(t, "123 claude --resume "+fakeSessionID("live")+"\n")
	task := getTask(t, "live")
	reconcileAutoRun(openFlowDB(t), task)
	if task.AutoRunStatus.String != "running" {
		t.Errorf("live tab run reconciled to %q, want running", task.AutoRunStatus.String)
	}
}

// Harness gone (tab closed / crashed) without flow done → dead, and the
// parent driver hears about it like a headless death.
func TestReconcileTabRunGoneIsDead(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "papa")
	seedTask(t, "gone")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET parent_slug='papa' WHERE slug='gone'`); err != nil {
		t.Fatal(err)
	}
	seedTabRun(t, "gone", time.Hour)
	stubPS(t, "1 bash\n")
	task := getTask(t, "gone")
	reconcileAutoRun(db, task)
	if task.AutoRunStatus.String != "dead" || !task.AutoRunFinished.Valid {
		t.Errorf("auto_run = %q finished=%v, want dead with finished set", task.AutoRunStatus.String, task.AutoRunFinished)
	}
	if got := getTask(t, "gone").AutoRunStatus.String; got != "dead" {
		t.Errorf("persisted auto_run_status = %q, want dead", got)
	}
	var body string
	if err := db.QueryRow(`SELECT body FROM bus_messages WHERE from_task_slug='gone' AND to_task_slug='papa'`).Scan(&body); err != nil {
		t.Fatalf("parent should be notified of a dead tab child: %v", err)
	}
	// A tab run has no auto-run log; point at the transcript instead.
	if strings.Contains(body, "auto-run log") || !strings.Contains(body, "flow transcript gone") {
		t.Errorf("dead-tab-child notice should point at the transcript, not a log; got %q", body)
	}
}

// Right after spawn the harness may not be in the process table yet.
func TestReconcileTabRunWithinGraceStaysRunning(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "young")
	seedTabRun(t, "young", 2*time.Second)
	stubPS(t, "1 bash\n")
	task := getTask(t, "young")
	reconcileAutoRun(openFlowDB(t), task)
	if task.AutoRunStatus.String != "running" {
		t.Errorf("young tab run reconciled to %q, want running (startup grace)", task.AutoRunStatus.String)
	}
}

// A done task's tab run is completed even though the tab (and harness)
// stay open for review.
func TestReconcileTabRunDoneIsCompleted(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "fin")
	seedTabRun(t, "fin", time.Hour)
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET status='done' WHERE slug='fin'`); err != nil {
		t.Fatal(err)
	}
	stubPS(t, "123 claude --resume "+fakeSessionID("fin")+"\n")
	task := getTask(t, "fin")
	reconcileAutoRun(db, task)
	if task.AutoRunStatus.String != "completed" {
		t.Errorf("done tab run reconciled to %q, want completed", task.AutoRunStatus.String)
	}
}

// flow done inside the tab records completed immediately and still runs
// the close-out sweep inline (there is no supervisor to defer to).
func TestCmdDoneCompletesTabRun(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "closer")
	seedTabRun(t, "closer", time.Minute)
	swept := stubClaudeRunner(t, nil)
	if rc := cmdDone([]string{"closer"}); rc != 0 {
		t.Fatalf("done rc=%d", rc)
	}
	task := getTask(t, "closer")
	if task.AutoRunStatus.String != "completed" || !task.AutoRunFinished.Valid {
		t.Errorf("auto_run = %q finished=%v, want completed", task.AutoRunStatus.String, task.AutoRunFinished)
	}
	if len(*swept) != 1 {
		t.Errorf("close-out sweeps = %d, want 1 (inline)", len(*swept))
	}
}

// A running tab child is still an auto run: it may not spawn grandchildren.
func TestTabRunIsAutoRunSession(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "kid")
	seedTabRun(t, "kid", time.Minute)
	t.Setenv(autoRunEnvVar, "")
	if !isAutoRunSession(getTask(t, "kid")) {
		t.Error("a running tab child must count as an auto-run session")
	}
}

func TestBuildAutoTabBootstrapPrompt(t *testing.T) {
	p := buildAutoTabBootstrapPrompt("kid", "regular", "", "papa")
	for _, want := range []string{"AUTONOMOUS", "terminal tab", "human may be watching", "flow done kid", "flow message user/papa", "PERSIST"} {
		if !strings.Contains(p, want) {
			t.Errorf("tab prompt missing %q", want)
		}
	}
	for _, bad := range []string{"NO HUMAN IS WATCHING", "headless"} {
		if strings.Contains(p, bad) {
			t.Errorf("tab prompt must not say %q", bad)
		}
	}
	if !strings.Contains(buildAutoBootstrapPrompt("kid", "regular", "", "papa"), "NO HUMAN IS WATCHING") {
		t.Error("headless prompt must keep its no-human wording")
	}
}

// flow auto --tab forwards to flow do --auto --tab.
func TestCmdAutoTabForwards(t *testing.T) {
	root := setupFlowRoot(t)
	_ = root
	count, _ := stubITerm(t)
	noHeadlessLaunch(t)
	brief := writeFile(t, t.TempDir(), "b.md", "# goal\nDone when: x\n")
	if rc := cmdAuto([]string{"tab child", "--slug", "tc", "--brief", brief, "--tab"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if *count != 1 {
		t.Errorf("tabs=%d, want 1", *count)
	}
	if getTask(t, "tc").AutoRunMode.String != autoRunModeTab {
		t.Error("flow auto --tab should record a tab-mode run")
	}
}

func TestShowTaskTabRun(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "shown")
	seedTabRun(t, "shown", time.Second)
	stubPS(t, "")
	out := captureStdout(t, func() { cmdShow([]string{"task", "shown"}) })
	if !strings.Contains(out, "running (tab") {
		t.Errorf("show should mark the run as a tab run; got:\n%s", out)
	}
}

// flow list marks a running tab run "running tab" (no pid to show).
func TestListTasksTabRun(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "listed")
	seedTabRun(t, "listed", time.Second)
	stubPS(t, "")
	out := captureStdout(t, func() {
		if rc := cmdList([]string{"tasks", "--no-color"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "running tab") {
		t.Errorf("AUTO column should read 'running tab'; got:\n%s", out)
	}
}

// A tab that only came from FLOW_AUTO_TAB is a preference: with no
// terminal it runs headless, and says so.
func TestCmdDoAutoTabEnvDefaultNoTerminalFallsBackLoudly(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "env-linux")
	pinNoTerminalHost(t)
	t.Setenv(autoTabEnvVar, "1")
	calls, _, _, _, _ := stubAutoLauncher(t, 55)
	var rc int
	out := captureStdout(t, func() { rc = cmdDo([]string{"env-linux", "--auto"}) })
	if rc != 0 {
		t.Fatalf("rc=%d; output:\n%s", rc, out)
	}
	if *calls != 1 {
		t.Errorf("headless launches = %d, want 1 (fallback)", *calls)
	}
	if !strings.Contains(out, "running headless instead") {
		t.Errorf("fallback must be announced; got:\n%s", out)
	}
	if m := getTask(t, "env-linux").AutoRunMode; m.Valid {
		t.Errorf("auto_run_mode = %q, want NULL (headless)", m.String)
	}
}

// Same for prx, which has no tab path yet.
func TestCmdDoAutoTabEnvDefaultPrxFallsBack(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "env-prx")
	t.Setenv(autoTabEnvVar, "1")
	count, _ := stubITerm(t)
	calls, _, _, _, _ := stubAutoLauncher(t, 56)
	var rc int
	out := captureStdout(t, func() { rc = cmdDo([]string{"env-prx", "--auto", "--harness", "prx"}) })
	if rc != 0 || *calls != 1 || *count != 0 {
		t.Errorf("rc=%d headless=%d tabs=%d, want 0/1/0; output:\n%s", rc, *calls, *count, out)
	}
	if !strings.Contains(out, "prx") || !strings.Contains(out, "running headless instead") {
		t.Errorf("fallback must name prx and say headless; got:\n%s", out)
	}
}
