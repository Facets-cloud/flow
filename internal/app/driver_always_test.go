package app

import (
	"flow/internal/flowdb"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Driver-always model (HUD #357): flow sets the `driver` tag itself on
// human adds and interactive opens; auto runs, playbook runs and owner
// ticks stay plain. One driver block (Go const) is injected for driver
// sessions; no Role section lives in brief.md.

func isDriver(t *testing.T, slug string) bool {
	t.Helper()
	db := openFlowDB(t)
	defer db.Close()
	return isDriverTask(db, slug)
}

func untag(t *testing.T, slug string) {
	t.Helper()
	db := openFlowDB(t)
	defer db.Close()
	if err := flowdb.RemoveTaskTag(db, slug, driverTag); err != nil {
		t.Fatal(err)
	}
}

func writeTaskBriefDA(t *testing.T, slug, body string) {
	t.Helper()
	p := filepath.Join(os.Getenv("FLOW_ROOT"), "tasks", slug, "brief.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------- auto-tag ----------

func TestAddTaskAutoTagsDriver(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "Human task", "--slug", "ht", "--work-dir", t.TempDir()}); rc != 0 {
		t.Fatal(rc)
	}
	if !isDriver(t, "ht") {
		t.Error("a human `flow add task` must be tagged driver by flow")
	}
}

func TestAddTaskInsideAutoRunOrOwnerTickNotTagged(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	t.Setenv(autoRunEnvVar, "parent")
	cmdAdd([]string{"task", "In auto", "--slug", "ia", "--work-dir", wd})
	t.Setenv(autoRunEnvVar, "")
	t.Setenv(ownerTickEnvVar, "own")
	cmdAdd([]string{"task", "In tick", "--slug", "it", "--work-dir", wd})
	t.Setenv(ownerTickEnvVar, "")
	cmdAdd([]string{"task", "Owned", "--slug", "ow", "--work-dir", wd, "--tag", "owner:own"})
	for _, s := range []string{"ia", "it", "ow"} {
		if isDriver(t, s) {
			t.Errorf("%s must stay plain (auto run / owner tick / owner-managed)", s)
		}
	}
}

func TestFlowAutoChildNotTagged(t *testing.T) {
	setupFlowRoot(t)
	stubAutoLauncher(t, 4242)
	brief := filepath.Join(t.TempDir(), "b.md")
	os.WriteFile(brief, []byte("goal: x\n"), 0o644)
	captureStdout(t, func() {
		if rc := cmdAuto([]string{"Child", "--slug", "kid", "--brief", brief, "--work-dir", t.TempDir()}); rc != 0 {
			t.Fatalf("auto rc=%d", rc)
		}
	})
	if isDriver(t, "kid") {
		t.Error("a flow auto child must never be tagged driver")
	}
}

func TestOwnerTickEnvCarriesMarker(t *testing.T) {
	found := false
	for _, kv := range ownerTickEnv("own") {
		if kv == ownerTickEnvVar+"=own" {
			found = true
		}
	}
	if !found {
		t.Errorf("owner tick env must carry %s", ownerTickEnvVar)
	}
}

func TestDoInteractiveAddsMissingTag(t *testing.T) {
	setupFlowRoot(t)
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}
	stubClaudeStatOK(t)
	stubITerm(t)
	seedTask(t, "old")
	untag(t, "old")
	captureStdout(t, func() {
		if rc := cmdDo([]string{"old"}); rc != 0 {
			t.Fatalf("do rc=%d", rc)
		}
	})
	if !isDriver(t, "old") {
		t.Error("interactive flow do must add the driver tag (lazy migration)")
	}
}

func TestDoAutoAndPlaybookRunNotTagged(t *testing.T) {
	setupFlowRoot(t)
	stubAutoLauncher(t, 4242)
	seedTask(t, "bg")
	untag(t, "bg")
	captureStdout(t, func() { cmdDo([]string{"bg", "--auto"}) })
	if isDriver(t, "bg") {
		t.Error("flow do --auto must not tag")
	}
	db := openFlowDB(t)
	seedTask(t, "run1")
	untag(t, "run1")
	if _, err := db.Exec(`UPDATE tasks SET kind='playbook_run' WHERE slug='run1'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if openTagDriver(openFlowDB(t), mustTask(t, "run1")) {
		t.Error("a playbook run must never be tagged")
	}
}

func mustTask(t *testing.T, slug string) *flowdb.Task {
	t.Helper()
	db := openFlowDB(t)
	defer db.Close()
	tk, err := flowdb.GetTask(db, slug)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestDoHereAddsTagAndPrintsBlock(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeStatOK(t)
	seedTaskAtCwd(t, "here")
	untag(t, "here")
	writeTaskBriefDA(t, "here", "# here\n\n## Goal\nx\n\n## Done when\n"+doneWhenPlaceholder+"\n")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "f00ba111-2222-4333-8444-555555555599")
	out := captureStdout(t, func() {
		if rc := cmdDo([]string{"here", "--here"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if !isDriver(t, "here") {
		t.Error("flow do --here must add the driver tag")
	}
	if !strings.Contains(out, "You own this task's outcome end to end") {
		t.Errorf("--here should print the driver block:\n%s", out)
	}
	if !strings.Contains(out, intakeTurnLine) {
		t.Errorf("--here should print the intake-turn line while the placeholder stands:\n%s", out)
	}
}

// ---------- one driver block ----------

func TestDriverBlockIsOneConst(t *testing.T) {
	b := driverRoleBlock()
	for _, want := range []string{
		"You own this task's outcome end to end", "Done when", "flow auto",
		"never block waiting for a reply", "Children cannot spawn grandchildren",
		"newer HOLD beats an older GO", "Needs from you:",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("driver block missing %q", want)
		}
	}
	for _, banned := range []string{"applies only while", "## Role", "brief.md Role"} {
		if strings.Contains(b, banned) {
			t.Errorf("driver block must not mention %q", banned)
		}
	}
}

func TestSessionStartDriverGetsBlockNoLightBlock(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv(autoRunEnvVar, "")
	seedTask(t, "drv")
	bindSession(t, "drv", "sid-drv")
	out := sessionStartOutput(t)
	if !strings.Contains(out, "You own this task's outcome end to end") {
		t.Errorf("driver session should get the driver block:\n%s", out)
	}
	if strings.Contains(out, "flow driver delegation") {
		t.Errorf("the light delegation block is gone:\n%s", out)
	}
	// An untagged bound session (e.g. not yet migrated) gets no light block either.
	seedTask(t, "pl")
	untag(t, "pl")
	bindSession(t, "pl", "sid-pl")
	if out := sessionStartOutput(t); strings.Contains(out, "flow driver delegation") || strings.Contains(out, "You own this task's outcome") {
		t.Errorf("untagged session gets no role block:\n%s", out)
	}
}

func TestSessionStartAutoRunGetsNoBlock(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "kid")
	bindSession(t, "kid", "sid-kid")
	t.Setenv(autoRunEnvVar, "kid")
	if out := sessionStartOutput(t); strings.Contains(out, "You own this task's outcome") {
		t.Errorf("auto run must not get the driver block:\n%s", out)
	}
}

// ---------- deterministic Done-when stop ----------

func TestIntakeTurnLineWhilePlaceholderStands(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv(autoRunEnvVar, "")
	seedTask(t, "drv")
	bindSession(t, "drv", "sid-drv")
	writeTaskBriefDA(t, "drv", "# drv\n\n## Goal\nx\n\n## Done when\n"+doneWhenPlaceholder+"\n")
	if out := sessionStartOutput(t); !strings.Contains(out, intakeTurnLine) {
		t.Errorf("placeholder → SessionStart must print the intake line:\n%s", out)
	}
	writeTaskBriefDA(t, "drv", "# drv\n\n## Goal\nx\n\n## Done when\n- tests green\n")
	if out := sessionStartOutput(t); strings.Contains(out, "INTAKE TURN") {
		t.Errorf("Done when written → no intake line:\n%s", out)
	}
	if intakeTurnLine != "INTAKE TURN: restate the goal, propose Done when + 'Needs from you:', then END the turn. Do not build or close anything." {
		t.Errorf("intake line text drifted: %q", intakeTurnLine)
	}
}

// ---------- resume pointer ----------

func TestResumePointerToUpgradeNotes(t *testing.T) {
	setupFlowRoot(t)
	clearSessionEnv(t)
	const sid = "cccccccc-2222-4333-8444-555555555555"
	db := openFlowDB(t)
	flowdb.RecordSessionVersion(db, sid, "v0.1.0-alpha.39", time.Now())
	db.Close()
	setVersion(t, "v0.1.0-alpha.40")
	run := func(source string) string {
		var out string
		withHookStdin(t, `{"session_id":"`+sid+`","source":"`+source+`"}`, func() {
			out = busHookContext(t, captureStdout(t, func() { cmdHookSessionStart(nil) }))
		})
		return out
	}
	if out := run("resume"); !strings.Contains(out, "upgrade-notes.md") || !strings.Contains(out, "v0.1.0-alpha.39") {
		t.Errorf("resume on a newer binary should point at upgrade-notes:\n%s", out)
	}
	// Recorded now: a second resume on the same binary is quiet.
	if out := run("resume"); strings.Contains(out, "upgrade-notes.md") {
		t.Errorf("pointer must not repeat once the session saw this version:\n%s", out)
	}
	db = openFlowDB(t)
	flowdb.RecordSessionVersion(db, sid, "v0.1.0-alpha.39", time.Now())
	db.Close()
	if out := run("startup"); strings.Contains(out, "upgrade-notes.md") {
		t.Errorf("startup is not a resume:\n%s", out)
	}
}

// ---------- hooks: unbound hint + nudge ----------

func TestUnboundSessionStartHintIsTaskFirst(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	ctx := busHookContext(t, captureStdout(t, func() { cmdHookSessionStart(nil) }))
	for _, want := range []string{
		"start a task",
		"FIRST",
		"before any other process skill",
		"brainstorming",
		"what goal do you want to achieve?",
		"which project",
		"never auto-create",
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

const nudgeMark = "create a task first"

func TestFirstPromptNudgeOncePerSession(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "11111111-1111-4111-8111-111111111111")
	first := promptSubmitCtx(t)
	if !strings.Contains(first, nudgeMark) {
		t.Fatalf("first unbound prompt should carry the nudge; got:\n%s", first)
	}
	if strings.Contains(first, "\n") {
		t.Errorf("nudge must be one line; got:\n%s", first)
	}
	if second := promptSubmitCtx(t); strings.Contains(second, nudgeMark) {
		t.Errorf("nudge must fire once per session id; got:\n%s", second)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "22222222-2222-4222-8222-222222222222")
	if other := promptSubmitCtx(t); !strings.Contains(other, nudgeMark) {
		t.Errorf("a new session id should get its own nudge; got:\n%s", other)
	}
}

func TestFirstPromptNudgeNeverInAutoRunOrBound(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "33333333-3333-4333-8333-333333333333")
	t.Setenv(autoRunEnvVar, "1")
	if got := promptSubmitCtx(t); strings.Contains(got, nudgeMark) {
		t.Errorf("auto run must never get the nudge; got:\n%s", got)
	}
	t.Setenv(autoRunEnvVar, "")
	const sid = "44444444-4444-4444-8444-444444444444"
	mkBusTask(t, openFlowDB(t), "bound-one", sid)
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)
	if got := promptSubmitCtx(t); strings.Contains(got, nudgeMark) {
		t.Errorf("bound session must not get the nudge; got:\n%s", got)
	}
}

func TestFirstPromptNudgeSkipsNonHumanPrompt(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Cleanup(func() { hookSessionID = "" })
	var got string
	withHookStdin(t, `{"session_id":"55555555-5555-4555-8555-555555555555","prompt":"<task-notification>x</task-notification>"}`,
		func() { got = promptSubmitCtx(t) })
	if strings.Contains(got, nudgeMark) {
		t.Errorf("non-human prompt must not get the nudge; got:\n%s", got)
	}
	withHookStdin(t, `{"session_id":"55555555-5555-4555-8555-555555555555","prompt":"I want a phonics app"}`,
		func() { got = promptSubmitCtx(t) })
	if !strings.Contains(got, nudgeMark) {
		t.Errorf("human first prompt should get the nudge; got:\n%s", got)
	}
}

// ---------- show project drivers: / done warning / project hint ----------

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
		if rc := cmdAdd([]string{"task", "Task " + slug, "--slug", slug, "--work-dir", wd, "--project", project}); rc != 0 {
			t.Fatalf("add task %s rc=%d", slug, rc)
		}
	}
	for _, s := range []string{"d1", "d2", "c1", "c2", "plain", "d4", "d5"} {
		add(s, "ph")
	}
	add("d3", "oth")
	for _, s := range []string{"c1", "c2", "plain"} {
		untag(t, s) // children / plain tasks (as an auto run would create them)
	}
	db := openFlowDB(t)
	defer db.Close()
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
	if i := strings.Index(out, "drivers:"); i >= 0 {
		return out[i:]
	}
	return ""
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
	if !regexp.MustCompile(`(?m)^  - Task d1 \(d1\) · in-progress · waiting: Rohit review · 1 open child$`).MatchString(block) {
		t.Errorf("d1 line wrong; got:\n%s", block)
	}
	if !regexp.MustCompile(`(?m)^      - Task d2 \(d2\) · backlog · 0 open children$`).MatchString(block) {
		t.Errorf("d2 should be indented under d1; got:\n%s", block)
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
	out := captureStdout(t, func() { showProjectCmd([]string{"ph", "--all"}) })
	block := driversBlock(out)
	if !strings.Contains(block, "(d4) · done") || strings.Contains(block, "(d5)") {
		t.Errorf("--all: done d4 in, archived d5 out; got:\n%s", block)
	}
}

func TestShowProjectNoDrivers(t *testing.T) {
	setupFlowRoot(t)
	cmdAdd([]string{"project", "Empty", "--slug", "em", "--work-dir", t.TempDir()})
	if out := captureStdout(t, func() { showProjectCmd([]string{"em"}) }); !strings.Contains(out, "drivers:     (none)") {
		t.Errorf("want drivers: (none); got:\n%s", out)
	}
}

func TestDoneDriverWarnsAboutOpenChildren(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	seedDriverProject(t)
	var rc int
	out := captureStdout(t, func() { rc = cmdDone([]string{"d1"}) })
	if rc != 0 {
		t.Fatalf("done must exit 0; rc=%d\n%s", rc, out)
	}
	for _, want := range []string{"warning: driver d1 has 2 open children", "c1 (in-progress)", "d2 (backlog)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "c2 (") {
		t.Errorf("done child c2 must not be listed; got:\n%s", out)
	}
	if out := captureStdout(t, func() { cmdDone([]string{"c1"}) }); strings.Contains(out, "open children") {
		t.Errorf("plain task: no warning; got:\n%s", out)
	}
}

const driverProjectHint = "drivers belong to a project; pass --project <slug>"

func TestAddTaskWithoutProjectHints(t *testing.T) {
	setupFlowRoot(t)
	var rc int
	out := captureStdout(t, func() {
		rc = cmdAdd([]string{"task", "Loose", "--slug", "ld", "--work-dir", t.TempDir()})
	})
	if rc != 0 || !strings.Contains(out, driverProjectHint) {
		t.Errorf("rc=%d; want project hint; got:\n%s", rc, out)
	}
	cmdAdd([]string{"project", "P", "--slug", "p", "--work-dir", t.TempDir()})
	if out := captureStdout(t, func() { cmdAdd([]string{"task", "Good", "--slug", "gd", "--project", "p"}) }); strings.Contains(out, driverProjectHint) {
		t.Errorf("no hint with --project; got:\n%s", out)
	}
	t.Setenv(autoRunEnvVar, "x")
	if out := captureStdout(t, func() { cmdAdd([]string{"task", "Kid", "--slug", "kd", "--work-dir", t.TempDir()}) }); strings.Contains(out, driverProjectHint) {
		t.Errorf("plain (auto-run) task: no hint; got:\n%s", out)
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

func flat(s string) string { return strings.Join(strings.Fields(s), " ") }

func TestSkillStartADriverOnePath(t *testing.T) {
	core := readSkill(t, "SKILL.md")
	s := flat(sectionOf(t, core, "### 4.2 Start a driver"))
	for _, want := range []string{
		"what goal do you want to achieve?",
		"which project does it belong in?",
		"always ask ONE project question",
		"by repo/work_dir path first, then by name/goal",
		"max 3, best first",
		"(Recommended)",
		`"New project <proposed name>"`,
		"free-text Other",
		"never auto-create silently",
		"never a \"no project\" or \"standalone\" option",
		"combine goal + project into ONE AskUserQuestion call (two questions)",
		"in a session with a parent task ask via `flow message`, never AskUserQuestion",
		"only when a live driver's goal really matches",
		"never override an explicit \"new\"",
		`flow add task "<short name>" --project <p>`,
		"flow tags it `driver` itself",
		"Done when the user already stated goes into `## Done when` now",
		`"Created driver task <name> in project <p>; continuing here."`,
		`"Created driver task <name> in project <p>; opened it in a new tab."`,
		`never say "I've tracked this as a task" or "tracked task"`,
		"intake turn ENDS",
		`"Needs from you:"`,
		"Backlog capture",
		"create it without binding",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("§4.2 missing %q", want)
		}
	}
	for _, banned := range []string{"How should this task run?", "Driver (Recommended)", "intake-task.md",
		"None (floating)", "Plain task instead", `flow add task "<short name>" --tag driver`, "## Role"} {
		if strings.Contains(s, banned) {
			t.Errorf("§4.2 must not contain %q", banned)
		}
	}
}

func TestSkillNoDriverOrNotQuestionsAnywhere(t *testing.T) {
	corpus := skillCorpus(t)
	for _, banned := range []string{"How should this task run?", "Driver (Recommended)", "Make it a driver",
		"references/intake-task.md", "### Intake question"} {
		if strings.Contains(corpus, banned) {
			t.Errorf("skill corpus still has %q", banned)
		}
	}
	if _, err := fs.ReadFile(skillFiles(), "references/intake-task.md"); err == nil {
		t.Error("references/intake-task.md must be removed")
	}
	for _, p := range []string{"SKILL.md", "references/templates.md", "references/delegation.md"} {
		if strings.Contains(readSkill(t, p), "## Role: driver") {
			t.Errorf("%s must not carry a Role section (rules are injected, not in brief.md)", p)
		}
	}
}

func TestSkillDoAndDelegationDescribeAutoTag(t *testing.T) {
	core := readSkill(t, "SKILL.md")
	do := flat(sectionOf(t, core, "### 4.4 Start / resume work on a task"))
	if strings.Contains(do, "make it a driver") {
		t.Error("§4.4 must not offer to make a task a driver")
	}
	a := flat(sectionOf(t, core, "### 4.4a"))
	for _, want := range []string{"flow tags", "auto runs", "do the work themselves"} {
		if !strings.Contains(a, want) {
			t.Errorf("§4.4a missing %q", want)
		}
	}
	del := readSkill(t, "references/delegation.md")
	if !strings.Contains(del, "## Writing a child's brief: useful shapes") {
		t.Error("delegation.md should keep the child-brief shapes")
	}
	un := flat(sectionOf(t, core, "### 4.14 Substantive-unrelated-work check"))
	if !strings.Contains(un, "Start a driver") {
		t.Error("§4.14 default action should be Start a driver")
	}
}

func TestSkillProjectIntakeFollowUpStartsADriver(t *testing.T) {
	ref := readSkill(t, "references/intake-project.md")
	if !strings.Contains(ref, "what do you want to achieve first?") || !strings.Contains(ref, "Start a driver") {
		t.Errorf("intake-project.md first follow-up should start a driver")
	}
}

func TestSkillTemplatesHaveDriverBriefWithoutRole(t *testing.T) {
	ref := readSkill(t, "references/templates.md")
	i := strings.Index(ref, "Driver brief")
	if i < 0 {
		t.Fatal("templates.md missing a Driver brief template")
	}
	tpl := ref[i:]
	last := -1
	for _, h := range []string{"## Goal", "## Done when", doneWhenPlaceholder, "## Context", "## Decisions"} {
		j := strings.Index(tpl, h)
		if j < 0 || j < last {
			t.Errorf("driver template: %q missing or out of order", h)
		}
		last = j
	}
}
