package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness/claude"
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

// HUD #408/#425: the pointer is ONE line naming the installed driver.md,
// and that exact path exists after a skill install, with the shipped text.
func TestDriverPointerNamesInstalledDriverMD(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv(autoRunEnvVar, "")
	if err := claude.New().InstallSkill(skillFiles()); err != nil {
		t.Fatal(err)
	}
	seedTask(t, "p")
	db := openFlowDB(t)
	tk, _ := flowdb.GetTask(db, "p")
	db.Close()
	blk := strings.TrimSpace(roleBlockFor(tk, false))
	const head = "You are the DRIVER for this task: follow references/driver.md ("
	if !strings.HasPrefix(blk, head) || !strings.HasSuffix(blk, ").") || strings.Contains(blk, "\n") {
		t.Fatalf("pointer must be one line %q…(<path>).; got %q", head, blk)
	}
	path := strings.TrimSuffix(strings.TrimPrefix(blk, head), ").")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("driver.md not installed at the pointer's path %q: %v", path, err)
	}
	want, _ := fs.ReadFile(skillFiles(), "references/driver.md")
	if string(got) != string(want) {
		t.Errorf("installed driver.md at %q differs from the shipped one", path)
	}
}
