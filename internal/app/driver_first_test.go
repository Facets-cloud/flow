package app

import (
	"flow/internal/flowdb"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// ---------- hooks ----------

// The unbound SessionStart hint is driver-first: work the user states
// becomes a driver task before any other process skill runs.
func TestUnboundSessionStartHintIsDriverFirst(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	ctx := busHookContext(t, captureStdout(t, func() { cmdHookSessionStart(nil) }))
	for _, want := range []string{
		"driver task",
		"Start a driver",
		"before any other process skill",
		"brainstorming",
		"what goal do you want to achieve?",
		"live driver",
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("unbound hint missing %q; got:\n%s", want, ctx)
		}
	}
}

func promptSubmitCtx(t *testing.T) string {
	t.Helper()
	out := captureStdout(t, func() {
		if rc := cmdHookUserPromptSubmit(nil); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if strings.TrimSpace(out) == "" {
		return ""
	}
	return busHookContext(t, out)
}

// The first prompt of an unbound session gets a one-line driver nudge,
// exactly once per session id.
func TestFirstPromptNudgeOncePerSession(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "11111111-1111-4111-8111-111111111111")
	first := promptSubmitCtx(t)
	if !strings.Contains(first, "driver task") || !strings.Contains(first, "Start a driver") {
		t.Fatalf("first unbound prompt should carry the driver nudge; got:\n%s", first)
	}
	if strings.Count(first, "\n") > 0 {
		t.Errorf("nudge must be one line; got:\n%s", first)
	}
	if second := promptSubmitCtx(t); strings.Contains(second, "Start a driver") {
		t.Errorf("nudge must fire once per session id; second prompt got:\n%s", second)
	}
	// A different session gets its own first nudge.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "22222222-2222-4222-8222-222222222222")
	if other := promptSubmitCtx(t); !strings.Contains(other, "Start a driver") {
		t.Errorf("a new session id should get its own nudge; got:\n%s", other)
	}
}

func TestFirstPromptNudgeNeverInAutoRun(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "33333333-3333-4333-8333-333333333333")
	t.Setenv(autoRunEnvVar, "1")
	if got := promptSubmitCtx(t); strings.Contains(got, "Start a driver") {
		t.Errorf("auto run must never get the nudge; got:\n%s", got)
	}
}

func TestFirstPromptNudgeNeverInBoundSession(t *testing.T) {
	setupFlowRoot(t)
	const sid = "44444444-4444-4444-8444-444444444444"
	mkBusTask(t, openFlowDB(t), "bound-one", sid)
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)
	got := promptSubmitCtx(t)
	if strings.Contains(got, "Start a driver") {
		t.Errorf("bound session must not get the nudge; got:\n%s", got)
	}
	if !strings.Contains(got, "flow session → task") {
		t.Errorf("bound session should still get its anchor; got:\n%s", got)
	}
}

func TestFirstPromptNudgeSkipsNonHumanPrompt(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Cleanup(func() { hookSessionID = "" })
	payload := `{"session_id":"55555555-5555-4555-8555-555555555555","prompt":"<task-notification>x</task-notification>"}`
	var got string
	withHookStdin(t, payload, func() { got = promptSubmitCtx(t) })
	if strings.Contains(got, "Start a driver") {
		t.Errorf("non-human prompt must not get the nudge; got:\n%s", got)
	}
	// ...and the marker is not spent: the human's first prompt still gets it.
	payload = `{"session_id":"55555555-5555-4555-8555-555555555555","prompt":"I want a phonics app"}`
	withHookStdin(t, payload, func() { got = promptSubmitCtx(t) })
	if !strings.Contains(got, "Start a driver") {
		t.Errorf("human first prompt should get the nudge; got:\n%s", got)
	}
}

// ---------- show project: drivers ----------

func seedDriverProject(t *testing.T) {
	t.Helper()
	wd := t.TempDir()
	if rc := cmdAdd([]string{"project", "Phonics", "--slug", "ph", "--work-dir", wd}); rc != 0 {
		t.Fatalf("add project rc=%d", rc)
	}
	if rc := cmdAdd([]string{"project", "Other", "--slug", "oth", "--work-dir", t.TempDir()}); rc != 0 {
		t.Fatalf("add project rc=%d", rc)
	}
	add := func(slug, project string) {
		args := []string{"task", "Task " + slug, "--slug", slug, "--work-dir", wd}
		if project != "" {
			args = append(args, "--project", project)
		}
		if rc := cmdAdd(args); rc != 0 {
			t.Fatalf("add task %s rc=%d", slug, rc)
		}
	}
	for _, s := range []string{"d1", "d2", "c1", "c2", "plain", "d4", "d5"} {
		add(s, "ph")
	}
	add("d3", "oth")
	db := openFlowDB(t)
	defer db.Close()
	for _, s := range []string{"d1", "d2", "d3", "d4", "d5"} {
		if err := flowdb.AddTaskTag(db, s, "driver"); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`UPDATE tasks SET status='in-progress', session_id='sid-d1', session_started='2026-01-01T00:00:00Z', waiting_on='Rohit review' WHERE slug='d1'`,
		`UPDATE tasks SET parent_slug='d1' WHERE slug IN ('d2','c1','c2')`,
		`UPDATE tasks SET status='in-progress', session_id='sid-c1', session_started='2026-01-01T00:00:00Z' WHERE slug='c1'`,
		`UPDATE tasks SET status='done', session_id='sid-'||slug, session_started='2026-01-01T00:00:00Z' WHERE slug IN ('c2','d4')`,
		`UPDATE tasks SET archived_at='2026-01-01T00:00:00Z' WHERE slug='d5'`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func driversBlock(out string) string {
	i := strings.Index(out, "drivers:")
	if i < 0 {
		return ""
	}
	return out[i:]
}

func TestShowProjectListsLiveDrivers(t *testing.T) {
	setupFlowRoot(t)
	seedDriverProject(t)
	out := captureStdout(t, func() {
		if rc := showProjectCmd([]string{"ph"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	block := driversBlock(out)
	if block == "" {
		t.Fatalf("show project missing drivers: block; got:\n%s", out)
	}
	d1 := regexp.MustCompile(`(?m)^  - Task d1 \(d1\) · in-progress · waiting: Rohit review · 1 open child$`)
	if !d1.MatchString(block) {
		t.Errorf("d1 line wrong; got:\n%s", block)
	}
	d2 := regexp.MustCompile(`(?m)^      - Task d2 \(d2\) · backlog · 0 open children$`)
	if !d2.MatchString(block) {
		t.Errorf("d2 should be indented under d1; got:\n%s", block)
	}
	if strings.Index(block, "(d2)") < strings.Index(block, "(d1)") {
		t.Errorf("sub-driver d2 must follow its parent d1; got:\n%s", block)
	}
	for _, absent := range []string{"(d3)", "(d4)", "(d5)", "(c1)", "(plain)"} {
		if strings.Contains(block, absent) {
			t.Errorf("drivers: must not list %s; got:\n%s", absent, block)
		}
	}
}

func TestShowProjectDriversAllIncludesDone(t *testing.T) {
	setupFlowRoot(t)
	seedDriverProject(t)
	out := captureStdout(t, func() {
		if rc := showProjectCmd([]string{"ph", "--all"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	block := driversBlock(out)
	if !strings.Contains(block, "(d4) · done") {
		t.Errorf("--all should include the done driver d4; got:\n%s", block)
	}
	if strings.Contains(block, "(d5)") {
		t.Errorf("--all must still hide archived drivers; got:\n%s", block)
	}
}

func TestShowProjectNoDrivers(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"project", "Empty", "--slug", "em", "--work-dir", t.TempDir()}); rc != 0 {
		t.Fatal(rc)
	}
	out := captureStdout(t, func() { showProjectCmd([]string{"em"}) })
	if !strings.Contains(out, "drivers:     (none)") {
		t.Errorf("want drivers: (none); got:\n%s", out)
	}
}

// ---------- done: open-children warning ----------

func TestDoneDriverWarnsAboutOpenChildren(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	seedDriverProject(t)
	var rc int
	out := captureStdout(t, func() { rc = cmdDone([]string{"d1"}) })
	if rc != 0 {
		t.Fatalf("done must still exit 0; rc=%d\n%s", rc, out)
	}
	if !strings.Contains(out, "warning: driver d1 has 2 open children") {
		t.Errorf("missing open-children warning; got:\n%s", out)
	}
	for _, want := range []string{"c1 (in-progress)", "d2 (backlog)"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning should list %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "c2 (") {
		t.Errorf("done child c2 must not be listed; got:\n%s", out)
	}
}

func TestDoneNonDriverNoChildrenWarning(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	seedDriverProject(t)
	// c1 is a plain task with no children.
	out := captureStdout(t, func() { cmdDone([]string{"c1"}) })
	if strings.Contains(out, "open children") {
		t.Errorf("no warning expected; got:\n%s", out)
	}
}

// ---------- skill corpus ----------

func readSkill(t *testing.T, p string) string {
	t.Helper()
	b, err := fs.ReadFile(skillFiles(), p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// sectionOf returns the text of the SKILL.md section starting at heading
// up to the next "### " heading.
func sectionOf(t *testing.T, doc, heading string) string {
	t.Helper()
	i := strings.Index(doc, heading)
	if i < 0 {
		t.Fatalf("heading %q not found", heading)
	}
	rest := doc[i+len(heading):]
	if j := strings.Index(rest, "\n### "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func TestSkillStartADriverIsTheUserPath(t *testing.T) {
	core := readSkill(t, "SKILL.md")
	s := sectionOf(t, core, "### 4.2 Start a driver")
	for _, want := range []string{
		"driver task",
		"what goal do you want to achieve?",
		"--tag driver",
		"live driver",
		"Done when",
		"references/intake-task.md",
		"flow do --here",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("§4.2 missing %q", want)
		}
	}
	for _, banned := range []string{"How should this task run?", "Driver (Recommended)", "Save it", "Tags?", "Detail now"} {
		if strings.Contains(s, banned) {
			t.Errorf("§4.2 user path must not ask %q", banned)
		}
	}
	// "goal" is a brief field, never an entity on the user path.
	for _, banned := range []string{"goal task", "a goal entity", "flow add goal", "the goal's", "goals:"} {
		if strings.Contains(strings.ToLower(core), banned) {
			t.Errorf("SKILL.md uses goal as an entity: %q", banned)
		}
	}
}

func TestSkillIntakeTaskReferenceKeepsFullInterview(t *testing.T) {
	ref := readSkill(t, "references/intake-task.md")
	for _, want := range []string{"How should this task run?", "Driver (Recommended)", "--tag driver", "Save it", "Tags?", "Open now?"} {
		if !strings.Contains(ref, want) {
			t.Errorf("intake-task.md missing %q", want)
		}
	}
}

func TestSkillDoOffersDriverUpgradeAndUnboundDefaultsToDriver(t *testing.T) {
	core := readSkill(t, "SKILL.md")
	do := sectionOf(t, core, "### 4.4 Start / resume work on a task")
	if !strings.Contains(do, "make it a driver") || !strings.Contains(do, "--tag driver") {
		t.Errorf("§4.4 should offer once to make a non-driver task a driver")
	}
	un := sectionOf(t, core, "### 4.14 Substantive-unrelated-work check")
	if !strings.Contains(un, "Start a driver") {
		t.Errorf("§4.14 default action should be Start a driver")
	}
}

func TestSkillProjectIntakeFollowUpStartsADriver(t *testing.T) {
	ref := readSkill(t, "references/intake-project.md")
	if !strings.Contains(ref, "what do you want to achieve first?") || !strings.Contains(ref, "Start a driver") {
		t.Errorf("intake-project.md first follow-up should start a driver")
	}
}

func TestSkillTemplatesHaveDriverBrief(t *testing.T) {
	ref := readSkill(t, "references/templates.md")
	i := strings.Index(ref, "Driver brief")
	if i < 0 {
		t.Fatal("templates.md missing a Driver brief template")
	}
	tpl := ref[i:]
	order := []string{"## Goal", "## Done when", "*Agree with the user on the first turn.*", "## Context", "## Decisions", "## Role: driver"}
	last := -1
	for _, h := range order {
		j := strings.Index(tpl, h)
		if j < 0 || j < last {
			t.Errorf("driver template: %q missing or out of order", h)
		}
		last = j
	}
}

// E2E finding (PR #181): with a detailed goal the driver built everything
// in the intake turn and said "I've tracked this as a task". Step 5's
// announcement is mandatory and verbatim; step 6 ends the intake turn
// after goal + proposed Done when + "Needs from you:".
func TestSkillStartADriverIntakeTurnStops(t *testing.T) {
	s := sectionOf(t, readSkill(t, "SKILL.md"), "### 4.2 Start a driver")
	flat := strings.Join(strings.Fields(s), " ")
	for _, want := range []string{
		"announcement is mandatory",
		`"Created driver task <name>; continuing here."`,
		`never "I've tracked this as a task"`,
		"intake turn ENDS",
		"propose Done when",
		`"Needs from you:"`,
		"No building or implementation in that turn, even when the goal is detailed",
		"unless the user already stated Done when",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("§4.2 missing %q", want)
		}
	}
}

// ---------- drivers always belong to a project ----------

const driverProjectHint = "drivers belong to a project; pass --project <slug>"

func TestAddDriverWithoutProjectHints(t *testing.T) {
	setupFlowRoot(t)
	var rc int
	out := captureStdout(t, func() {
		rc = cmdAdd([]string{"task", "Loose driver", "--slug", "ld", "--work-dir", t.TempDir(), "--tag", "driver"})
	})
	if rc != 0 {
		t.Fatalf("add must still succeed; rc=%d\n%s", rc, out)
	}
	if !strings.Contains(out, driverProjectHint) {
		t.Errorf("missing project hint; got:\n%s", out)
	}
	db := openFlowDB(t)
	defer db.Close()
	if _, err := flowdb.GetTask(db, "ld"); err != nil {
		t.Errorf("task should still be created: %v", err)
	}
}

func TestAddDriverWithProjectNoHint(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"project", "P", "--slug", "p", "--work-dir", wd}); rc != 0 {
		t.Fatal(rc)
	}
	out := captureStdout(t, func() {
		cmdAdd([]string{"task", "Good driver", "--slug", "gd", "--project", "p", "--tag", "driver"})
	})
	if strings.Contains(out, driverProjectHint) {
		t.Errorf("no hint expected with --project; got:\n%s", out)
	}
	// A plain floating task gets no hint either.
	out = captureStdout(t, func() {
		cmdAdd([]string{"task", "Plain", "--slug", "pl", "--work-dir", wd})
	})
	if strings.Contains(out, driverProjectHint) {
		t.Errorf("no hint expected for a plain task; got:\n%s", out)
	}
}

func TestUpdateTagDriverOnFloatingTaskHints(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"task", "Floater", "--slug", "fl", "--work-dir", wd}); rc != 0 {
		t.Fatal(rc)
	}
	var rc int
	out := captureStdout(t, func() { rc = cmdUpdate([]string{"task", "fl", "--tag", "driver"}) })
	if rc != 0 {
		t.Fatalf("update must still succeed; rc=%d\n%s", rc, out)
	}
	if !strings.Contains(out, driverProjectHint) {
		t.Errorf("missing project hint; got:\n%s", out)
	}
	if rc := cmdAdd([]string{"project", "P", "--slug", "p", "--work-dir", wd}); rc != 0 {
		t.Fatal(rc)
	}
	if rc := cmdAdd([]string{"task", "In project", "--slug", "ip", "--project", "p"}); rc != 0 {
		t.Fatal(rc)
	}
	out = captureStdout(t, func() { cmdUpdate([]string{"task", "ip", "--tag", "driver"}) })
	if strings.Contains(out, driverProjectHint) {
		t.Errorf("no hint expected for a task in a project; got:\n%s", out)
	}
}

func TestSkillStartADriverAlwaysHasAProject(t *testing.T) {
	s := sectionOf(t, readSkill(t, "SKILL.md"), "### 4.2 Start a driver")
	flat := strings.Join(strings.Fields(s), " ")
	for _, want := range []string{
		"A driver task ALWAYS belongs to a project",
		"by repo/work_dir path, then by name/goal",
		"ask only if 2+ fit",
		"CREATE one without asking",
		`flow add project "<name>"`,
		"thin brief",
		`"Created project <p> and driver task <name>; continuing here."`,
		"skip the first-task follow-up in references/intake-project.md",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("§4.2 missing %q", want)
		}
	}
	if strings.Contains(s, "None (floating)") {
		t.Error("§4.2 driver path must not offer None (floating)")
	}
}
