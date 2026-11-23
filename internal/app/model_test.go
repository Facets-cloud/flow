package app

import (
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
)

func taskModel(t *testing.T, slug string) (model, effort string) {
	t.Helper()
	task, err := flowdb.GetTask(openFlowDB(t), slug)
	if err != nil {
		t.Fatal(err)
	}
	return task.Model.String, task.Effort.String
}

// No flags: the spawn carries no model flag and nothing is stored.
func TestCmdDoNoModelFlagsUnchanged(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "plain")
	stubNewUUID(t, "658bf2be-5ae3-4842-a8a4-e0d0b785514d")
	_, script := stubITerm(t)
	if rc := cmdDo([]string{"plain"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if s := script(); strings.Contains(s, "--model") || strings.Contains(s, "--effort") {
		t.Errorf("spawn without flags must not carry model flags: %s", s)
	}
	if m, e := taskModel(t, "plain"); m != "" || e != "" {
		t.Errorf("stored model=%q effort=%q, want empty", m, e)
	}
}

// A flag on spawn is stored; a resume without flags reuses it; a flag on
// resume overrides and updates only that value.
func TestCmdDoModelPersistsAndOverrides(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "mt")
	stubNewUUID(t, "658bf2be-5ae3-4842-a8a4-e0d0b785514d")
	stubClaudeStatOK(t)
	_, script := stubITerm(t)

	if rc := cmdDo([]string{"mt", "--model", "haiku", "--effort", "low"}); rc != 0 {
		t.Fatalf("spawn rc=%d", rc)
	}
	if s := script(); !strings.Contains(s, "--model 'haiku' --effort 'low'") {
		t.Errorf("spawn missing model flags: %s", s)
	}
	if m, e := taskModel(t, "mt"); m != "haiku" || e != "low" {
		t.Errorf("stored model=%q effort=%q", m, e)
	}

	if rc := cmdDo([]string{"mt", "--force"}); rc != 0 {
		t.Fatalf("resume rc=%d", rc)
	}
	if s := script(); !strings.Contains(s, "--resume") || !strings.Contains(s, "--model 'haiku' --effort 'low'") {
		t.Errorf("resume must reuse stored values: %s", s)
	}

	if rc := cmdDo([]string{"mt", "--force", "--model", "opus"}); rc != 0 {
		t.Fatalf("override rc=%d", rc)
	}
	if s := script(); !strings.Contains(s, "--model 'opus' --effort 'low'") {
		t.Errorf("override must use the new model and keep effort: %s", s)
	}
	if m, e := taskModel(t, "mt"); m != "opus" || e != "low" {
		t.Errorf("stored after override model=%q effort=%q", m, e)
	}
}

func TestModelFlagsRejectedWithHere(t *testing.T) {
	setupFlowRoot(t)
	for _, args := range [][]string{
		{"task", "--here", "--model", "opus"},
		{"task", "--here", "--effort", "low"},
	} {
		stderr := captureStderr(t)
		if rc := cmdDo(args); rc != 2 {
			t.Errorf("cmdDo %v rc=%d, want 2", args, rc)
		}
		if got := stderr(); !strings.Contains(got, "--model/--effort cannot be used with --here") {
			t.Errorf("stderr = %q", got)
		}
	}
	if rc := cmdAdd([]string{"playbook", "Triage", "--slug", "tri", "--work-dir", t.TempDir()}); rc != 0 {
		t.Fatal("add playbook")
	}
	if rc := cmdRun([]string{"playbook", "tri", "--here", "--model", "opus"}); rc != 2 {
		t.Errorf("run playbook --here --model rc=%d, want 2", rc)
	}
}

// noModelHarness is a harness without the ModelSelector capability.
type noModelHarness struct{ harness.Harness }

func TestCheckModelFlagsUnsupportedHarness(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "nm")
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "nm")
	if err != nil {
		t.Fatal(err)
	}
	h := noModelHarness{claude.New()}
	stderr := captureStderr(t)
	if rc := checkModelFlags(task, h, "opus", ""); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
	if got := stderr(); !strings.Contains(got, "does not support --model/--effort") {
		t.Errorf("stderr = %q", got)
	}
	if rc := checkModelFlags(task, h, "", ""); rc != 0 {
		t.Errorf("no flags on unsupported harness rc=%d, want 0", rc)
	}
}

// A command refused by the running-auto-run guard must not change the
// task's stored model/effort.
func TestCmdDoRefusedAutoKeepsModel(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ra")
	stubITerm(t)
	stubAutoLauncher(t, 4242)
	oldAlive := processAlive
	processAlive = func(pid int) bool { return true }
	t.Cleanup(func() { processAlive = oldAlive })

	if rc := cmdDo([]string{"ra", "--auto", "--model", "haiku", "--effort", "low"}); rc != 0 {
		t.Fatalf("first --auto rc=%d", rc)
	}
	if rc := cmdDo([]string{"ra", "--auto", "--model", "opus", "--effort", "high"}); rc != 1 {
		t.Fatalf("second --auto rc=%d, want 1 (already running)", rc)
	}
	if m, e := taskModel(t, "ra"); m != "haiku" || e != "low" {
		t.Errorf("refused command changed stored model=%q effort=%q", m, e)
	}
}

// An --effort-only launch writes only effort: a model another command
// stored after this one loaded the task survives.
func TestStoreModelFlagsWritesOnlyGivenColumns(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "sc")
	db := openFlowDB(t)
	task := &flowdb.Task{Slug: "sc"}
	if err := storeModelFlags(db, task, "haiku", "low"); err != nil {
		t.Fatal(err)
	}
	if err := storeModelFlags(db, &flowdb.Task{Slug: "sc"}, "opus", ""); err != nil { // concurrent --model
		t.Fatal(err)
	}
	if err := storeModelFlags(db, task, "", "high"); err != nil { // --effort only
		t.Fatal(err)
	}
	if m, e := taskModel(t, "sc"); m != "opus" || e != "high" {
		t.Errorf("stored model=%q effort=%q, want opus/high", m, e)
	}
	// task carries the merged row, not its stale haiku.
	if task.Model.String != "opus" || task.Effort.String != "high" {
		t.Errorf("task model=%q effort=%q, want opus/high", task.Model.String, task.Effort.String)
	}
}

// A background launch uses the merged stored values: a model another
// command stored after this one loaded the task reaches the spawn argv.
func TestCmdDoBackgroundUsesMergedModel(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bgr")
	stubBGMode(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET model='haiku', effort='low' WHERE slug='bgr'`); err != nil {
		t.Fatal(err)
	}
	task, err := flowdb.GetTask(db, "bgr")
	if err != nil {
		t.Fatal(err)
	}
	// Another command stores --model opus after this one read the task.
	if _, err := db.Exec(`UPDATE tasks SET model='opus' WHERE slug='bgr'`); err != nil {
		t.Fatal(err)
	}
	if rc := cmdDoBackground(db, task, defaultHarness(), bgLaunch{effort: "high"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !pairArg(c.lastSpawn, "--model", "opus") || !pairArg(c.lastSpawn, "--effort", "high") {
		t.Errorf("bg spawn argv want opus/high: %v", c.lastSpawn)
	}
	if m, e := taskModel(t, "bgr"); m != "opus" || e != "high" {
		t.Errorf("stored model=%q effort=%q, want opus/high", m, e)
	}
}

func TestCmdDoRejectsUnsafeModelValue(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bad")
	stubITerm(t)
	if rc := cmdDo([]string{"bad", "--model", "-p"}); rc != 2 {
		t.Errorf("rc=%d, want 2", rc)
	}
}

// --auto stores the values and the supervisor hands them to the harness.
func TestCmdDoAutoCarriesModel(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "am")
	stubITerm(t)
	stubAutoLauncher(t, 4242)
	if rc := cmdDo([]string{"am", "--auto", "--model", "haiku", "--effort", "low"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if m, e := taskModel(t, "am"); m != "haiku" || e != "low" {
		t.Fatalf("stored model=%q effort=%q", m, e)
	}
	seedRunningAuto(t, "am", 4242)
	var got harness.LaunchOpts
	stubAutoRunner(t, func(h harness.Harness, sid, prompt string, opts harness.LaunchOpts) error {
		got = opts
		return nil
	})
	logPath := autoTestLog(t, "am")
	if rc := cmdAutoExec([]string{"am", "--log", logPath}); rc != 0 {
		t.Fatalf("cmdAutoExec rc=%d", rc)
	}
	if got.Model != "haiku" || got.Effort != "low" {
		t.Errorf("supervisor opts model=%q effort=%q", got.Model, got.Effort)
	}
}

func TestCmdDoBackgroundCarriesModel(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "bgm")
	stubBGMode(t)
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	c := stubBGCommand(t, &reg)
	if rc := cmdDo([]string{"bgm", "--model", "opus", "--effort", "high"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !pairArg(c.lastSpawn, "--model", "opus") || !pairArg(c.lastSpawn, "--effort", "high") {
		t.Errorf("bg spawn argv missing model flags: %v", c.lastSpawn)
	}
}

func TestFlowAutoForwardsModel(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	stubAutoLauncher(t, 1)
	bp := writeBrief(t, "do it\n")
	if rc := cmdAuto([]string{"Cheap Child", "--slug", "cheap", "--brief", bp, "--model", "haiku", "--effort", "low"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if m, e := taskModel(t, "cheap"); m != "haiku" || e != "low" {
		t.Errorf("stored model=%q effort=%q", m, e)
	}
	// A bad value is refused before the task is created.
	if rc := cmdAuto([]string{"Bad Child", "--slug", "bad-child", "--brief", bp, "--effort", "very high"}); rc != 2 {
		t.Errorf("bad effort rc=%d, want 2", rc)
	}
	if _, err := flowdb.GetTask(openFlowDB(t), "bad-child"); err == nil {
		t.Error("a refused --effort must not leave a task behind")
	}
}

func TestRunPlaybookForwardsModel(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"playbook", "Triage", "--slug", "tri", "--work-dir", t.TempDir()}); rc != 0 {
		t.Fatal("add playbook")
	}
	_, script := stubITerm(t)
	if rc := cmdRun([]string{"playbook", "tri", "--model", "sonnet"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if s := script(); !strings.Contains(s, "--model 'sonnet'") {
		t.Errorf("playbook run spawn missing --model: %s", s)
	}
}

func TestShowTaskPrintsModel(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "shown")
	out := captureStdout(t, func() { cmdShow([]string{"task", "shown"}) })
	if strings.Contains(out, "model:") || strings.Contains(out, "effort:") {
		t.Errorf("unset model/effort must not print:\n%s", out)
	}
	if _, err := openFlowDB(t).Exec(`UPDATE tasks SET model='haiku', effort='low' WHERE slug='shown'`); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() { cmdShow([]string{"task", "shown"}) })
	if !strings.Contains(out, "model:                 haiku") || !strings.Contains(out, "effort:                low") {
		t.Errorf("show must print model/effort:\n%s", out)
	}
}
