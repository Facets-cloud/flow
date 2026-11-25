package app

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
	"flow/internal/harness/codex"
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

func busHookContextLine(out, marker string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, marker) {
			return l
		}
	}
	return ""
}

// The pointer names ONE installed driver.md, for the session's own harness,
// even with both harnesses installed; the file exists there as shipped.
func TestDriverPointerNamesInstalledDriverMD(t *testing.T) {
	setupFlowRoot(t)
	env := withHarnessEnv(t, "claude", "codex") // isolates PATH discovery
	t.Setenv(autoRunEnvVar, "")
	for _, h := range []harness.Harness{claude.New(), codex.New()} {
		if err := h.InstallSkill(skillFiles()); err != nil {
			t.Fatal(err)
		}
	}
	want, _ := fs.ReadFile(skillFiles(), "references/driver.md")
	for name, skillMD := range map[string]string{"claude": env.claudeSkill(), "codex": env.codexSkill()} {
		seedTask(t, "p-"+name)
		writeTaskBriefDM(t, "p-"+name, agreedBrief)
		db := openFlowDB(t)
		db.Exec(`UPDATE tasks SET harness=? WHERE slug=?`, name, "p-"+name)
		tk, _ := flowdb.GetTask(db, "p-"+name)
		db.Close()
		blk := strings.TrimSpace(roleBlockFor(tk, false))
		const head = "You are the DRIVER for this task: follow references/driver.md ("
		end := strings.Index(blk, ").")
		if !strings.HasPrefix(blk, head) || end < 0 || strings.Contains(blk, "\n") {
			t.Fatalf("%s: pointer must be one line %q<path>).; got %q", name, head, blk)
		}
		path := blk[len(head):end]
		if wantPath := filepath.Join(filepath.Dir(skillMD), "references", "driver.md"); path != wantPath {
			t.Errorf("%s: pointer path %q, want the %s install %q", name, path, name, wantPath)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != string(want) {
			t.Errorf("%s: driver.md not installed at %q: %v", name, path, err)
		}
	}
}

// A human `flow add task` gets the driver brief stub (INTAKE TURN fires on
// open); adds from an auto run (env or a bound running auto task) and
// owner-managed adds (owner:<slug>) keep the plain stub.
func TestAddTaskWritesDriverStub(t *testing.T) {
	setupFlowRoot(t)
	clearSessionEnv(t)
	t.Setenv(autoRunEnvVar, "")
	add := func(slug string, extra ...string) string {
		t.Helper()
		args := append([]string{"task", "T " + slug, "--slug", slug, "--work-dir", t.TempDir()}, extra...)
		if rc := cmdAdd(args); rc != 0 {
			t.Fatalf("add %s rc=%d", slug, rc)
		}
		b, err := os.ReadFile(filepath.Join(os.Getenv("FLOW_ROOT"), "tasks", slug, "brief.md"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	driver := func(slug string) string {
		return "# T " + slug + "\n\nRole: driver, follow flow skill references/driver.md\n\n## Goal\n\n## Done when\n" +
			doneWhenPlaceholder + "\n\n## Context\n\n## Decisions\n"
	}
	plain := func(slug string) string { return fmt.Sprintf(taskBriefStub, "T "+slug) }
	if got := add("sb"); got != driver("sb") {
		t.Errorf("human add: got %q", got)
	}
	if got := add("emptyowner", "--tag", "owner:"); got != driver("emptyowner") {
		t.Errorf("`owner:` with no slug names no owner: want the driver stub, got %q", got)
	}
	if got := add("spaced", "--tag", "owner: ci"); got != driver("spaced") {
		t.Errorf("`owner: ci` is not the ledger's owner:ci tag: want the driver stub, got %q", got)
	}
	if got := add("ow", "--tag", "owner:keeper"); got != plain("ow") {
		t.Errorf("owner-managed add: got %q", got)
	}
	// A legacy auto run: no env marker, but the bound task's run is running.
	db := openFlowDB(t)
	db.Exec(`UPDATE tasks SET session_id='sid-run', session_started=?, status='in-progress', auto_run_status='running' WHERE slug='sb'`, flowdb.NowISO())
	db.Close()
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-run")
	if got := add("legacykid"); got != plain("legacykid") {
		t.Errorf("add from a running auto task: got %q", got)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv(autoRunEnvVar, "parent")
	if got := add("ch"); got != plain("ch") {
		t.Errorf("add inside an auto run: got %q", got)
	}
}

// INTAKE TURN checks the Done-when section only: a placeholder quoted under
// Decisions is history, not an open intake.
func TestIntakeTurnChecksDoneWhenSectionOnly(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "q")
	writeTaskBriefDM(t, "q", "## Done when\n- tests green\n\n## Decisions\n- replaced "+doneWhenPlaceholder+"\n")
	if strings.Contains(intakeTurnBlock("q"), "INTAKE TURN") {
		t.Error("placeholder outside ## Done when must not reopen intake")
	}
	writeTaskBriefDM(t, "q", "## Done when\n"+doneWhenPlaceholder+"\n\n## Context\n")
	if !strings.Contains(intakeTurnBlock("q"), "INTAKE TURN") {
		t.Error("placeholder in ## Done when must print the intake line")
	}
}

// A compaction-triggered SessionStart (Codex source=compact) arrives
// mid-turn: it must not mark a busy session idle.
func TestSessionStartCompactKeepsBusy(t *testing.T) {
	setupFlowRoot(t)
	clearSessionEnv(t)
	t.Setenv(autoRunEnvVar, "")
	const sid = "abababab-1111-4111-8111-111111111111"
	seedTask(t, "c")
	bindDM(t, "c", sid, true)
	db := openFlowDB(t)
	flowdb.SetSessionState(db, "c", flowdb.SessionBusy, time.Now())
	db.Close()
	if out := sessionStartWith(t, sid, "compact"); !strings.Contains(out, driverRoleMarker) {
		t.Fatalf("compact must reach the bound driver path:\n%s", out)
	}
	db = openFlowDB(t)
	defer db.Close()
	if st, _ := flowdb.GetSessionState(db, "c"); st.State != flowdb.SessionBusy {
		t.Errorf("compact SessionStart changed state to %q", st.State)
	}
}

// `do --here --force` switching a Codex task to this Claude session points
// at the Claude install, not the stale pre-bind harness.
func TestDoHereForceSwitchPointsAtNewHarness(t *testing.T) {
	setupFlowRoot(t)
	env := withHarnessEnv(t, "claude", "codex")
	stubClaudeStatOK(t)
	t.Setenv(autoRunEnvVar, "")
	seedTaskAtCwd(t, "sw")
	writeTaskBriefDM(t, "sw", agreedBrief)
	db := openFlowDB(t)
	db.Exec(`UPDATE tasks SET harness='codex' WHERE slug='sw'`)
	db.Close()
	t.Setenv("CLAUDE_CODE_SESSION_ID", "cdcdcdcd-1111-4111-8111-111111111111")
	out := captureStdout(t, func() { cmdDo([]string{"sw", "--here", "--force"}) })
	want := filepath.Join(filepath.Dir(env.claudeSkill()), "references", "driver.md")
	if !strings.Contains(out, "("+want+").") {
		t.Errorf("pointer must name the Claude install %q:\n%s", want, out)
	}
}

// A compaction-triggered SessionStart must not swallow a pending upgrade
// notice: the session did not reload the skill.
func TestCompactSessionStartKeepsUpgradeNotice(t *testing.T) {
	setupFlowRoot(t)
	clearSessionEnv(t)
	const sid = "efefefef-1111-4111-8111-111111111111"
	setVersion(t, "v0.1.0-alpha.40")
	hookStart(t, sid) // records alpha.40
	setVersion(t, "v0.1.0-alpha.41")
	withHookStdin(t, `{"session_id":"`+sid+`","source":"compact"}`, func() {
		captureStdout(t, func() { cmdHookSessionStart(nil) })
	})
	if out := hookUPS(t, sid); !strings.Contains(out, upgradeMarker) || !strings.Contains(out, "v0.1.0-alpha.40 → v0.1.0-alpha.41") {
		t.Errorf("the next prompt after a compact must still get the upgrade notice:\n%s", out)
	}
}

// add.go and the dead-run ping agree with the owner ledger: only the exact
// `owner:<slug>` form is owner-managed.
func TestOwnerManagedMatchesLedgerForm(t *testing.T) {
	setupFlowRoot(t)
	for tag, want := range map[string]bool{"owner:ci": true, "owner: ci": false, "owner:": false} {
		seedTask(t, "o")
		db := openFlowDB(t)
		flowdb.AddTaskTag(db, "o", tag)
		if got := isOwnerManaged(db, "o"); got != want {
			t.Errorf("isOwnerManaged(%q) = %v, want %v", tag, got, want)
		}
		db.Exec(`DELETE FROM task_tags WHERE task_slug='o'`)
		db.Exec(`DELETE FROM tasks WHERE slug='o'`)
		db.Close()
	}
}
