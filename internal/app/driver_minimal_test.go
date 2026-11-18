package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

// Minimal driver model (Ask #135): every bound, human-driven session that
// is not a playbook run gets the driver block; no tag is needed or written.

func writeTaskBriefDM(t *testing.T, slug, body string) {
	t.Helper()
	p := filepath.Join(os.Getenv("FLOW_ROOT"), "tasks", slug, "brief.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const placeholderBrief = "# t\n\n## Goal\nx\n\n## Done when\n" + doneWhenPlaceholder + "\n"
const agreedBrief = "# t\n\n## Goal\nx\n\n## Done when\n- tests green\n"

func sessionStartWith(t *testing.T, sid, source string) string {
	t.Helper()
	var out string
	withHookStdin(t, `{"session_id":"`+sid+`","source":"`+source+`"}`, func() {
		out = busHookContext(t, captureStdout(t, func() { cmdHookSessionStart(nil) }))
	})
	return out
}

func bindDM(t *testing.T, slug, sid string, resumed bool) {
	t.Helper()
	db := openFlowDB(t)
	defer db.Close()
	q := `UPDATE tasks SET session_id=?, session_started=?, status='in-progress' WHERE slug=?`
	if _, err := db.Exec(q, sid, flowdb.NowISO(), slug); err != nil {
		t.Fatal(err)
	}
	if resumed {
		db.Exec(`UPDATE tasks SET session_last_resumed=? WHERE slug=?`, flowdb.NowISO(), slug)
	}
}

func TestEveryBoundHumanSessionGetsDriverBlock(t *testing.T) {
	setupFlowRoot(t)
	clearSessionEnv(t)
	t.Setenv(autoRunEnvVar, "")
	seedTask(t, "plain") // no driver tag
	bindDM(t, "plain", "aaaaaaaa-1111-4111-8111-111111111111", true)
	out := sessionStartWith(t, "aaaaaaaa-1111-4111-8111-111111111111", "resume")
	if !strings.Contains(out, driverRoleMarker) {
		t.Errorf("an untagged bound session must get the driver block:\n%s", out)
	}
	if strings.Contains(out, "flow driver delegation") {
		t.Errorf("the light delegation block must not be injected:\n%s", out)
	}
}

func TestPlaybookRunAndAutoRunGetNoDriverBlock(t *testing.T) {
	setupFlowRoot(t)
	clearSessionEnv(t)
	t.Setenv(autoRunEnvVar, "")
	seedTask(t, "run1")
	db := openFlowDB(t)
	db.Exec(`UPDATE tasks SET kind='playbook_run' WHERE slug='run1'`)
	db.Close()
	bindDM(t, "run1", "bbbbbbbb-1111-4111-8111-111111111111", true)
	if out := sessionStartWith(t, "bbbbbbbb-1111-4111-8111-111111111111", "resume"); strings.Contains(out, driverRoleMarker) {
		t.Errorf("a playbook run must not get the driver block:\n%s", out)
	}
	seedTask(t, "kid")
	bindDM(t, "kid", "cccccccc-1111-4111-8111-111111111111", true)
	t.Setenv(autoRunEnvVar, "kid")
	if out := sessionStartWith(t, "cccccccc-1111-4111-8111-111111111111", "resume"); strings.Contains(out, driverRoleMarker) {
		t.Errorf("an auto run must not get the driver block:\n%s", out)
	}
}

func TestNoTagWrittenBySessionStartOrDoHere(t *testing.T) {
	setupFlowRoot(t)
	clearSessionEnv(t)
	stubClaudeStatOK(t)
	t.Setenv(autoRunEnvVar, "")
	seedTaskAtCwd(t, "here")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "dddddddd-1111-4111-8111-111111111111")
	out := captureStdout(t, func() {
		if rc := cmdDo([]string{"here", "--here"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, driverRoleMarker) {
		t.Errorf("--here must print the driver block for an untagged task:\n%s", out)
	}
	db := openFlowDB(t)
	defer db.Close()
	if isDriverTask(db, "here") {
		t.Error("no tag writes: --here must not tag the task")
	}
}

func TestIntakeTurnWhilePlaceholderStands(t *testing.T) {
	setupFlowRoot(t)
	clearSessionEnv(t)
	t.Setenv(autoRunEnvVar, "")
	const sid = "eeeeeeee-1111-4111-8111-111111111111"
	seedTask(t, "drv")
	bindDM(t, "drv", sid, true)
	writeTaskBriefDM(t, "drv", placeholderBrief)
	if out := sessionStartWith(t, sid, "resume"); !strings.Contains(out, intakeTurnLine) {
		t.Errorf("placeholder → intake line:\n%s", out)
	}
	writeTaskBriefDM(t, "drv", agreedBrief)
	if out := sessionStartWith(t, sid, "resume"); strings.Contains(out, "INTAKE TURN") {
		t.Errorf("Done when agreed → no intake line:\n%s", out)
	}
	if intakeTurnLine != "INTAKE TURN: restate the goal, propose Done when + 'Needs from you:', then END the turn. Do not build or close anything." {
		t.Errorf("intake line text drifted: %q", intakeTurnLine)
	}
}

func TestIntakeTurnOnFirstStartupAndFirstBind(t *testing.T) {
	setupFlowRoot(t)
	clearSessionEnv(t)
	stubClaudeStatOK(t)
	t.Setenv(autoRunEnvVar, "")
	const sid = "ffffffff-1111-4111-8111-111111111111"
	seedTask(t, "spawned")
	bindDM(t, "spawned", sid, false) // spawned, never resumed
	writeTaskBriefDM(t, "spawned", agreedBrief)
	if out := sessionStartWith(t, sid, "startup"); !strings.Contains(out, intakeTurnLine) {
		t.Errorf("first startup → intake line whatever the brief says:\n%s", out)
	}
	if out := sessionStartWith(t, sid, "resume"); strings.Contains(out, "INTAKE TURN") {
		t.Errorf("resume with Done when agreed → no intake line:\n%s", out)
	}

	seedTaskAtCwd(t, "fresh")
	writeTaskBriefDM(t, "fresh", agreedBrief)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "12121212-1111-4111-8111-111111111111")
	out := captureStdout(t, func() {
		if rc := cmdDo([]string{"fresh", "--here"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, intakeTurnLine) {
		t.Errorf("first --here bind → intake line whatever the brief says:\n%s", out)
	}
}

func TestUnboundHintIsTaskFirst(t *testing.T) {
	setupFlowRoot(t)
	clearSessionEnv(t)
	ctx := busHookContext(t, captureStdout(t, func() { cmdHookSessionStart(nil) }))
	for _, want := range []string{"start a task via flow FIRST", "brainstorm inside it", "Start a driver"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("unbound hint missing %q:\n%s", want, ctx)
		}
	}
}

// ---------- skill + docs ----------

func readSkillDM(t *testing.T, p string) string {
	t.Helper()
	b, err := fs.ReadFile(skillFiles(), p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func sectionDM(t *testing.T, doc, heading string) string {
	t.Helper()
	i := strings.Index(doc, heading)
	if i < 0 {
		t.Fatalf("heading %q not found", heading)
	}
	rest := doc[i+len(heading):]
	if j := strings.Index(rest, "\n### "); j >= 0 {
		rest = rest[:j]
	}
	return strings.Join(strings.Fields(rest), " ")
}

func TestSkillStartADriver(t *testing.T) {
	core := readSkillDM(t, "SKILL.md")
	s := sectionDM(t, core, "### 4.2 Start a driver")
	for _, want := range []string{
		"what goal do you want to achieve?",
		"which project does it belong in?",
		"always ask ONE project question",
		"by repo/work_dir path first, then by name/goal",
		`always include "New project <proposed name>"`,
		"free-text Other",
		`never a "no project" or "standalone" option`,
		"only when a live driver's goal really matches",
		`never override an explicit "new"`,
		`flow add task "<short name>" --project <p>`,
		`"Created driver task <name> in project <p>; continuing here."`,
		"keep the placeholder until the user agrees",
		"intake turn ENDS",
		`"Needs from you:"`,
		"Backlog capture",
		"create it without binding",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("§4.2 missing %q", want)
		}
	}
	for _, banned := range []string{"How should this task run?", "Driver (Recommended)", "Regular (this session only)",
		"Make it a driver", "None (floating)", "Other bound sessions delegate lightly"} {
		if strings.Contains(core, banned) {
			t.Errorf("SKILL.md still has %q", banned)
		}
	}
	if strings.Contains(readSkillDM(t, "references/delegation.md"), "How should this task run?") {
		t.Error("delegation.md still asks the Driver? question")
	}
}

func TestDocsDriverTemplateAndTrustAll(t *testing.T) {
	tpl := readSkillDM(t, "references/templates.md")
	i := strings.Index(tpl, "Driver brief")
	if i < 0 {
		t.Fatal("templates.md missing the Driver brief template")
	}
	last := -1
	for _, h := range []string{"## Goal", "## Done when", doneWhenPlaceholder, "## Context", "## Decisions"} {
		j := strings.Index(tpl[i:], h)
		if j < 0 || j < last {
			t.Errorf("driver template: %q missing or out of order", h)
		}
		last = j
	}
	for _, p := range []string{"../../README.md", "../../INSTALL.md"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"Trust all"`) {
			t.Errorf(`%s must tell Codex users to pick "Trust all"`, p)
		}
	}
}

func busHookContextLine(out, marker string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, marker) {
			return l
		}
	}
	return ""
}

// HUD #408: the injection is ONE line pointing at the installed driver.md.
func TestDriverPointerIsOneLineWithInstalledPath(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "p")
	tk := func() *flowdb.Task {
		db := openFlowDB(t)
		defer db.Close()
		x, _ := flowdb.GetTask(db, "p")
		return x
	}()
	t.Setenv(autoRunEnvVar, "")
	blk := roleBlockFor(tk, false)
	if !strings.HasPrefix(strings.TrimSpace(blk), "You are the DRIVER for this task: follow references/driver.md (") {
		t.Errorf("pointer shape wrong: %q", blk)
	}
	if !strings.Contains(blk, filepath.Join("references", "driver.md")) || strings.Contains(blk, "upgrade-notes") {
		t.Errorf("pointer must name the installed driver.md: %q", blk)
	}
	if strings.Contains(strings.TrimSpace(blk), "\n") {
		t.Errorf("pointer must be one line: %q", blk)
	}
}

// HUD #408/#409: the driver rules live in references/driver.md.
func TestDriverMDCarriesTheRules(t *testing.T) {
	d := strings.Join(strings.Fields(readSkillDM(t, "references/driver.md")), " ")
	for _, want := range []string{
		"own this task's outcome end to end", "flow auto", "spawning auto child: <slug>",
		"no grandchildren", "idle", "Every child brief states", "re-ask it with `flow do --auto <slug> --with",
		"Verify each child's output yourself", "newer HOLD beats an older GO", "dev lead",
		"never AskUserQuestion", "Needs from you:", "headless auto run", "do the work yourself",
		"offer to close", "never block waiting for a reply",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("driver.md missing %q", want)
		}
	}
	if strings.ContainsRune(d, '—') {
		t.Error("driver.md contains an em dash")
	}
}

// HUD #409: every interactive brief carries ONE Role line; SKILL.md tells
// the agent to read driver.md when it sees it.
func TestRoleLineInBriefAndSkillDirection(t *testing.T) {
	const roleLine = "Role: driver, follow flow skill references/driver.md"
	core := readSkillDM(t, "SKILL.md")
	if !strings.Contains(sectionDM(t, core, "### 4.2 Start a driver"), roleLine) {
		t.Errorf("§4.2 intake must write %q into the brief", roleLine)
	}
	if !strings.Contains(strings.Join(strings.Fields(core), " "), "If your brief says `Role: driver`, read references/driver.md.") {
		t.Error("SKILL.md must direct: If your brief says `Role: driver`, read references/driver.md.")
	}
	tpl := readSkillDM(t, "references/templates.md")
	if !strings.Contains(tpl[strings.Index(tpl, "Driver brief"):], roleLine) {
		t.Errorf("templates.md driver brief must include %q", roleLine)
	}
	if strings.Contains(core, "## Role: driver") || strings.Contains(tpl, "## Role: driver") {
		t.Error("a Role line, not a Role section")
	}
}
