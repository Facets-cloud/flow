package app

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
	"flow/internal/harness/prx"
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

// prx: the tab runs `prx -session-id <id>` interactively in bypass mode
// with the autonomous prompt.
func TestCmdDoAutoTabSpawnsTabPrx(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "prx-tab")
	count, script := stubITerm(t)
	noHeadlessLaunch(t)
	if rc := cmdDo([]string{"prx-tab", "--auto", "--tab", "--harness", "prx"}); rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
	if *count != 1 {
		t.Fatalf("tab spawns = %d, want 1", *count)
	}
	task := getTask(t, "prx-tab")
	s := script()
	for _, want := range []string{"prx -session-id " + task.SessionID.String, "-permission-mode", "-prompt", "AUTONOMOUS", "-trust-workspace"} {
		if !strings.Contains(s, want) {
			t.Errorf("prx tab command missing %q; got:\n%s", want, s)
		}
	}
	if strings.Contains(s, "prx run") {
		t.Errorf("tab must run interactive prx, not prx run; got:\n%s", s)
	}
	if task.AutoRunStatus.String != "running" || task.AutoRunMode.String != autoRunModeTab || task.Harness.String != "prx" {
		t.Errorf("auto_run=%q/%q harness=%q, want running/tab/prx", task.AutoRunStatus.String, task.AutoRunMode.String, task.Harness.String)
	}
}

// stubCloseoutLauncher records detached close-out sweep launches.
func stubCloseoutLauncher(t *testing.T) *[]string {
	t.Helper()
	var got []string
	old := closeoutLauncher
	closeoutLauncher = func(slug string) error {
		got = append(got, slug)
		return nil
	}
	t.Cleanup(func() { closeoutLauncher = old })
	return &got
}

// On a harness that defers the sweep in auto runs (prx, codex), a tab
// run's own flow done has no supervisor to defer to: it detaches the
// sweep into its own process instead of running it inline.
func TestCmdDoneTabRunDeferringHarnessDetachesSweep(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "prx-closer")
	seedTabRun(t, "prx-closer", time.Minute)
	if _, err := openFlowDB(t).Exec(`UPDATE tasks SET harness='prx' WHERE slug='prx-closer'`); err != nil {
		t.Fatal(err)
	}
	inline := stubPrxSweep(t)
	launched := stubCloseoutLauncher(t)
	out := captureStdout(t, func() {
		if rc := cmdDone([]string{"prx-closer"}); rc != 0 {
			t.Fatalf("done rc=%d", rc)
		}
	})
	if *inline != 0 {
		t.Errorf("inline sweeps = %d, want 0 (detached)", *inline)
	}
	if len(*launched) != 1 || (*launched)[0] != "prx-closer" {
		t.Errorf("detached sweep launches = %v, want [prx-closer]", *launched)
	}
	if !strings.Contains(out, "detached") {
		t.Errorf("done should say the sweep was detached; got:\n%s", out)
	}
	if got := getTask(t, "prx-closer").AutoRunStatus.String; got != "completed" {
		t.Errorf("auto_run_status = %q, want completed", got)
	}
}

// The hidden __closeout-sweep entry point runs the sweep for a task.
func TestCmdCloseoutSweepRunsSweep(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "swept")
	seedTabRun(t, "swept", time.Minute)
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET harness='prx', status='done' WHERE slug='swept'`); err != nil {
		t.Fatal(err)
	}
	inline := stubPrxSweep(t)
	if rc := cmdCloseoutSweep([]string{"swept"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if *inline != 1 {
		t.Errorf("sweeps = %d, want 1", *inline)
	}
}

func stubPrxSweep(t *testing.T) *int {
	t.Helper()
	var n int
	old := prx.SkipPermissionsRunner
	prx.SkipPermissionsRunner = func(string, harness.LaunchOpts) error { n++; return nil }
	t.Cleanup(func() { prx.SkipPermissionsRunner = old })
	return &n
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

// B3 / review item 6: a tab must run the same flow binary that launched
// it, without copying the launcher's PATH. The tab's own shell evaluates
// PATH=<flowdir>:"$PATH", so only the flow dir is prepended to whatever
// the tab's login shell set; FLOW_BIN is the unresolved launcher path.
func TestCmdDoAutoTabPinsLauncherBinary(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "pinned")
	binDir := t.TempDir()
	stubTabFlowBin(t, filepath.Join(binDir, "flow"))
	t.Setenv("PATH", "/shadow/bin:/usr/bin")
	_, script := stubITerm(t)
	noHeadlessLaunch(t)
	if rc := cmdDo([]string{"pinned", "--auto", "--tab", "--harness", "prx"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	s := script()
	for _, want := range []string{
		"FLOW_BIN='" + filepath.Join(binDir, "flow") + "'",
		"PATH='" + binDir + "':",
		"$PATH",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("tab spawn missing %q; got:\n%s", want, s)
		}
	}
	if strings.Contains(s, "/shadow/bin") {
		t.Errorf("the launcher's PATH must not be copied into the tab; got:\n%s", s)
	}
}

// pinnedFlowEnv carries FLOW_BIN (+ FLOW_ROOT) only: no PATH value.
func TestPinnedFlowEnvHasNoPath(t *testing.T) {
	stubTabFlowBin(t, "/opt/flow/bin/flow")
	t.Setenv("PATH", "/a:/b")
	t.Setenv("FLOW_ROOT", "")
	env := pinnedFlowEnv()
	if _, ok := env["PATH"]; ok {
		t.Errorf("pinnedFlowEnv must not carry PATH; got %q", env["PATH"])
	}
	if env["FLOW_BIN"] != "/opt/flow/bin/flow" {
		t.Errorf("FLOW_BIN = %q", env["FLOW_BIN"])
	}
	if _, ok := env["FLOW_ROOT"]; ok {
		t.Error("FLOW_ROOT must not be set when the launcher has none")
	}
	if got := withFlowOnPath("claude x"); got != `PATH='/opt/flow/bin':"$PATH" claude x` {
		t.Errorf("withFlowOnPath = %q", got)
	}
}

// FLOW_BIN is not symlink-resolved: a Homebrew Cellar path dies after
// brew upgrade + cleanup, the /opt/homebrew/bin symlink does not.
func TestTabFlowBinPathKeepsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "Cellar-flow")
	if err := os.WriteFile(real, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "flow")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	old := executablePath
	executablePath = func() (string, error) { return link, nil }
	t.Cleanup(func() { executablePath = old })
	if got := tabFlowBinPath(); got != link {
		t.Errorf("tabFlowBinPath = %q, want the unresolved %q", got, link)
	}
}

// The no-terminal handoff prints a short PATH prefix, not the launcher's
// whole PATH.
func TestNoTerminalHandoffHasNoLauncherPath(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "linux-path")
	pinNoTerminalHost(t)
	stubTabFlowBin(t, "/opt/flow/bin/flow")
	t.Setenv("PATH", "/very/long/launcher/marker:/usr/bin")
	var rc int
	out := captureStdout(t, func() { rc = cmdDo([]string{"linux-path"}) })
	if rc != 0 {
		t.Fatalf("rc=%d; output:\n%s", rc, out)
	}
	if strings.Contains(out, "/very/long/launcher/marker") {
		t.Errorf("handoff must not print the launcher's PATH; got:\n%s", out)
	}
	if !strings.Contains(out, `PATH='/opt/flow/bin':"$PATH"`) {
		t.Errorf("handoff should prepend only the flow dir; got:\n%s", out)
	}
}

func stubTabFlowBin(t *testing.T, bin string) {
	t.Helper()
	old := tabFlowBinPath
	tabFlowBinPath = func() string { return bin }
	t.Cleanup(func() { tabFlowBinPath = old })
}

// B4: a tab run falsely reconciled dead must still complete on its own
// flow done, and a deferring harness must still detach the sweep (never
// sweep inline inside the prx tool).
func TestCmdDoneDeadTabRunCompletesAndDetaches(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "lazarus")
	seedTabRun(t, "lazarus", time.Minute)
	if _, err := openFlowDB(t).Exec(`UPDATE tasks SET harness='prx', auto_run_status='dead', auto_run_finished=? WHERE slug='lazarus'`, flowdb.NowISO()); err != nil {
		t.Fatal(err)
	}
	inline := stubPrxSweep(t)
	launched := stubCloseoutLauncher(t)
	captureStdout(t, func() {
		if rc := cmdDone([]string{"lazarus"}); rc != 0 {
			t.Fatalf("done rc=%d", rc)
		}
	})
	if *inline != 0 {
		t.Errorf("inline sweeps = %d, want 0 (detached)", *inline)
	}
	if len(*launched) != 1 {
		t.Errorf("detached sweep launches = %v, want [lazarus]", *launched)
	}
	if got := getTask(t, "lazarus").AutoRunStatus.String; got != "completed" {
		t.Errorf("auto_run_status = %q, want completed", got)
	}
}

// B5: flow auto --mkdir creating a brand-new work dir outside the flow
// root: flow made it, so the prx tab launch passes -trust-workspace instead
// of stalling on prx's trust prompt.
func TestCmdAutoTabMkdirTrustsCreatedDir(t *testing.T) {
	setupFlowRoot(t)
	_, script := stubITerm(t)
	noHeadlessLaunch(t)
	dir := filepath.Join(t.TempDir(), "fresh", "wd")
	brief := writeFile(t, t.TempDir(), "b.md", "# goal\nDone when: x\n")
	if rc := cmdAuto([]string{"mk child", "--slug", "mk", "--brief", brief, "--tab", "--harness", "prx", "--work-dir", dir, "--mkdir"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if s := script(); !strings.Contains(s, "-trust-workspace") {
		t.Errorf("prx tab in a flow-created --mkdir dir should pass -trust-workspace; got:\n%s", s)
	}
}

// ...but an existing user dir is never auto-trusted, --mkdir or not.
func TestCmdAutoTabMkdirExistingDirNotTrusted(t *testing.T) {
	setupFlowRoot(t)
	_, script := stubITerm(t)
	noHeadlessLaunch(t)
	dir := t.TempDir()
	brief := writeFile(t, t.TempDir(), "b.md", "# goal\nDone when: x\n")
	if rc := cmdAuto([]string{"ex child", "--slug", "ex", "--brief", brief, "--tab", "--harness", "prx", "--work-dir", dir, "--mkdir"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if s := script(); strings.Contains(s, "-trust-workspace") {
		t.Errorf("an existing user dir must keep prx's trust prompt; got:\n%s", s)
	}
}

// Review round (Pramod): tab runs get the headless guards, flow-injected
// prompt recognition, resume/bootstrap fallback, hermetic env default.

func TestTabRunSpawnEnvMarksAutoRun(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "tab-env")
	stubNewUUID(t, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	_, script := stubITerm(t)
	noHeadlessLaunch(t)
	if rc := cmdDo([]string{"tab-env", "--auto", "--tab", "--harness", "prx"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if s := script(); !strings.Contains(s, "FLOW_AUTO_RUN='tab-env'") {
		t.Errorf("tab env missing FLOW_AUTO_RUN; got:\n%s", s)
	}
}

func TestTabBootstrapPromptIsNotHumanPrompt(t *testing.T) {
	p := buildAutoTabBootstrapPrompt("x", "regular", "", "")
	if isHumanPrompt(true, p, func(string) string { return "" }) {
		t.Error("tab bootstrap prompt counted as human-typed")
	}
}

func TestAutoTabDefaultIgnoredInsideAutoRun(t *testing.T) {
	t.Setenv(autoTabEnvVar, "1")
	t.Setenv(autoRunEnvVar, "")
	if !autoTabDefault() {
		t.Fatal("want tab default outside an auto run")
	}
	t.Setenv(autoRunEnvVar, "owner")
	if autoTabDefault() {
		t.Error("an unattended run must never default to a tab")
	}
}

func TestCloseoutNotDeferredForTabRun(t *testing.T) {
	t.Setenv(autoRunEnvVar, "tt")
	task := &flowdb.Task{Slug: "tt", AutoRunMode: sql.NullString{String: autoRunModeTab, Valid: true}}
	if closeoutDeferred(claude.New(), task) {
		t.Error("tab run has no supervisor; sweep must not be deferred")
	}
}

func TestCmdDoAutoTabResumeMarkerOnce(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "tab-once")
	stubClaudeStatOK(t)
	sid := fakeSessionID("tab-once")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET status='in-progress', session_id=?, session_started=?, harness='claude' WHERE slug='tab-once'`,
		sid, flowdb.NowISO()); err != nil {
		t.Fatal(err)
	}
	_, script := stubITerm(t)
	noHeadlessLaunch(t)
	if rc := cmdDo([]string{"tab-once", "--auto", "--tab", "--with", "x"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if n := strings.Count(script(), harness.InjectionMarker); n != 1 {
		t.Errorf("injection marker count = %d, want 1", n)
	}
}

// A reserved session with no transcript must launch via bootstrap, not --resume.
func TestCmdDoAutoTabResumeMissingTranscriptBootstraps(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "tab-nodisk")
	stubClaudeStatMissing(t)
	sid := fakeSessionID("tab-nodisk")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET status='in-progress', session_id=?, session_started=?, harness='claude' WHERE slug='tab-nodisk'`,
		sid, flowdb.NowISO()); err != nil {
		t.Fatal(err)
	}
	_, script := stubITerm(t)
	noHeadlessLaunch(t)
	if rc := cmdDo([]string{"tab-nodisk", "--auto", "--tab"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	s := script()
	if strings.Contains(s, "--resume") || !strings.Contains(s, "--session-id "+sid) {
		t.Errorf("want bootstrap with --session-id %s, got:\n%s", sid, s)
	}
}

func TestCmdAutoTabMkdirRelativeWorkDirTrusted(t *testing.T) {
	setupFlowRoot(t)
	_, script := stubITerm(t)
	noHeadlessLaunch(t)
	t.Chdir(t.TempDir())
	brief := writeFile(t, t.TempDir(), "b.md", "# goal\nDone when: x\n")
	if rc := cmdAuto([]string{"rel child", "--slug", "rel", "--brief", brief, "--tab", "--harness", "prx", "--work-dir", "newdir", "--mkdir"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if s := script(); !strings.Contains(s, "-trust-workspace") {
		t.Errorf("relative --mkdir dir should be trusted; got:\n%s", s)
	}
}

func TestCmdDoAutoTabRunningGuardSaysTab(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "tab-g")
	stubNewUUID(t, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	stubITerm(t)
	noHeadlessLaunch(t)
	if rc := cmdDo([]string{"tab-g", "--auto", "--tab"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	read := captureStderr(t)
	cmdDo([]string{"tab-g", "--auto", "--tab"})
	errOut := read()
	if strings.Contains(errOut, "pid 0") || !strings.Contains(errOut, "tab run") {
		t.Errorf("guard message = %q", errOut)
	}
}

// A tab launch that commits between done's load and its flip must still
// be seen as a tab run: the flip's RETURNING re-reads auto_run_mode, so a
// deferring harness detaches the sweep instead of running it inline.
func TestCmdDoneSeesTabModeCommittedBeforeFlip(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "late-tab")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET status='in-progress', session_id=?, session_started=?, harness='prx' WHERE slug='late-tab'`,
		fakeSessionID("late-tab"), flowdb.NowISO()); err != nil {
		t.Fatal(err)
	}
	t.Setenv(autoRunEnvVar, "")
	old := beforeDoneFlip
	beforeDoneFlip = func() {
		if _, err := db.Exec(`UPDATE tasks SET auto_run_status='running', auto_run_mode='tab', auto_run_pid=NULL,
			auto_run_started=? WHERE slug='late-tab'`, flowdb.NowISO()); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { beforeDoneFlip = old })
	inline := stubPrxSweep(t)
	launched := stubCloseoutLauncher(t)
	if rc := cmdDone([]string{"late-tab"}); rc != 0 {
		t.Fatalf("done rc=%d", rc)
	}
	if *inline != 0 || len(*launched) != 1 {
		t.Errorf("inline=%d detached=%v, want 0 inline and 1 detached (fresh auto_run_mode from the flip)", *inline, *launched)
	}
}

// #175 × --tab: --model/--effort on `flow auto --tab` reach the tab's
// interactive launch command and are stored on the task, so a later tab
// resume reuses them.
func TestCmdAutoTabPassesModelEffort(t *testing.T) {
	setupFlowRoot(t)
	_, script := stubITerm(t)
	noHeadlessLaunch(t)
	brief := writeFile(t, t.TempDir(), "b.md", "# goal\nDone when: x\n")
	if rc := cmdAuto([]string{"tab model", "--slug", "tm", "--brief", brief, "--tab", "--model", "opus-test", "--effort", "high"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	s := script()
	for _, want := range []string{"--model 'opus-test'", "--effort 'high'", "AUTONOMOUS"} {
		if !strings.Contains(s, want) {
			t.Errorf("tab command missing %q; got:\n%s", want, s)
		}
	}
	task := getTask(t, "tm")
	if task.Model.String != "opus-test" || task.Effort.String != "high" || task.AutoRunMode.String != autoRunModeTab {
		t.Errorf("model=%q effort=%q mode=%q, want opus-test/high/tab", task.Model.String, task.Effort.String, task.AutoRunMode.String)
	}
}

// The resume path of a tab run keeps the task's stored model.
func TestCmdDoAutoTabResumeKeepsModel(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "tab-model-resume")
	stubClaudeStatOK(t)
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET status='in-progress', session_id=?, session_started=?, harness='claude', model='sonnet-test'
		WHERE slug='tab-model-resume'`, fakeSessionID("tab-model-resume"), flowdb.NowISO()); err != nil {
		t.Fatal(err)
	}
	_, script := stubITerm(t)
	noHeadlessLaunch(t)
	if rc := cmdDo([]string{"tab-model-resume", "--auto", "--tab"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if s := script(); !strings.Contains(s, "--resume ") || !strings.Contains(s, "--model 'sonnet-test'") {
		t.Errorf("resumed tab command should carry the stored model; got:\n%s", s)
	}
}

// FLOW_AUTO_TAB is a human's interactive preference. Detached supervisors
// and owner ticks are unattended, so their env drops it: anything they
// dispatch with --auto stays headless instead of opening tabs.
func TestAutoChildEnvStripsAutoTab(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv(autoTabEnvVar, "1") // setupFlowRoot clears it; set it here
	for _, kv := range autoChildEnv() {
		if strings.HasPrefix(kv, autoTabEnvVar+"=") {
			t.Fatalf("autoChildEnv kept %q", kv)
		}
	}

	db := openFlowDB(t)
	if err := flowdb.CreateOwner(db, &flowdb.Owner{
		Slug: "tabby-owner", Name: "O", WorkDir: t.TempDir(), Every: "30m", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	var got []string
	old := ownerTickLauncher
	ownerTickLauncher = func(slug, workDir, logPath string, env []string) (int, error) { got = env; return 9, nil }
	t.Cleanup(func() { ownerTickLauncher = old })
	if rc := ownerTickManual([]string{"tabby-owner", "--auto"}); rc != 0 {
		t.Fatalf("owner tick --auto rc=%d", rc)
	}
	if got == nil {
		t.Fatal("owner tick launcher not called")
	}
	for _, kv := range got {
		if strings.HasPrefix(kv, autoTabEnvVar+"=") {
			t.Errorf("owner tick env kept %q: a tick could open tabs", kv)
		}
	}
}

// Item 5: a dir flow just created with --mkdir is trusted per launch only
// when it is NOT inside a git repo (the FlowOwnedRoot rule): a new folder
// inside someone's repo would inherit that repo's hooks, so a human must
// still answer the trust prompt. TestCmdAutoTabMkdirTrustsCreatedDir is
// the outside-a-repo half.
func TestCmdAutoTabMkdirInsideGitRepoNotTrusted(t *testing.T) {
	setupFlowRoot(t)
	_, script := stubITerm(t)
	noHeadlessLaunch(t)
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(repo, "new", "wd")
	brief := writeFile(t, t.TempDir(), "b.md", "# goal\nDone when: x\n")
	if rc := cmdAuto([]string{"git child", "--slug", "gitmk", "--brief", brief, "--tab", "--harness", "prx", "--work-dir", dir, "--mkdir"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if s := script(); strings.Contains(s, "-trust-workspace") {
		t.Errorf("a --mkdir dir inside a git repo must keep prx's trust prompt; got:\n%s", s)
	}
}
