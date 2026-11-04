package app

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness"
)

// driverRoleMarker is the heading of the stronger block a task tagged
// `driver` gets. Regular bound tasks keep the light delegation block
// (delegationMarker); tests key on the markers so wording can evolve.
const driverRoleMarker = "flow DRIVER task"

func tagTask(t *testing.T, slug, tag string) {
	t.Helper()
	if err := flowdb.AddTaskTag(openFlowDB(t), slug, tag); err != nil {
		t.Fatal(err)
	}
}

func TestSessionStartDriverTaskGetsDriverBlock(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	seedTask(t, "drv")
	tagTask(t, "drv", "driver")
	bindSession(t, "drv", "sid-drv")
	out := sessionStartOutput(t)
	if !strings.Contains(out, driverRoleMarker) {
		t.Fatalf("driver task should get the driver block, got:\n%s", out)
	}
	for _, want := range []string{"end to end", "flow auto", "Needs from you", "Agent"} {
		if !strings.Contains(out, want) {
			t.Errorf("driver block missing %q", want)
		}
	}
}

func TestSessionStartRegularTaskKeepsLightBlock(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	seedTask(t, "plain")
	bindSession(t, "plain", "sid-plain")
	out := sessionStartOutput(t)
	if strings.Contains(out, driverRoleMarker) {
		t.Errorf("regular task must not get the driver block:\n%s", out)
	}
	if !strings.Contains(out, delegationMarker) {
		t.Errorf("regular task keeps the light delegation block:\n%s", out)
	}
}

func TestSessionStartDriverTaggedAutoChildGetsNoDriverBlock(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "kid")
	tagTask(t, "kid", "driver")
	bindSession(t, "kid", "sid-kid")
	t.Setenv("FLOW_AUTO_RUN", "kid")
	if out := sessionStartOutput(t); strings.Contains(out, driverRoleMarker) {
		t.Errorf("auto child must never get the driver block:\n%s", out)
	}
}

func TestDoHerePrintsRoleBlock(t *testing.T) {
	for _, tc := range []struct {
		name, want, notWant string
		driver              bool
	}{
		{"driver", driverRoleMarker, "", true},
		{"regular", delegationMarker, driverRoleMarker, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupFlowRoot(t)
			t.Setenv("FLOW_AUTO_RUN", "")
			seedTaskAtCwd(t, "here-task")
			if tc.driver {
				tagTask(t, "here-task", "driver")
			}
			stubClaudeStatOK(t)
			t.Setenv("CLAUDE_CODE_SESSION_ID", "f00ba111-2222-4333-8444-555555555555")
			stubITerm(t)
			var rc int
			out := captureStdout(t, func() { rc = cmdDo([]string{"here-task", "--here"}) })
			if rc != 0 {
				t.Fatalf("rc=%d", rc)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("--here output missing %q:\n%s", tc.want, out)
			}
			if tc.notWant != "" && strings.Contains(out, tc.notWant) {
				t.Errorf("--here output should not contain %q:\n%s", tc.notWant, out)
			}
		})
	}
}

func TestUpdateTagDriverOnBoundTaskPrintsDriverBlock(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	seedTask(t, "mid")
	bindSession(t, "mid", "sid-mid")
	var rc int
	out := captureStdout(t, func() { rc = cmdUpdate([]string{"task", "mid", "--tag", "driver"}) })
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(out, driverRoleMarker) {
		t.Errorf("tagging the bound task driver should switch this session now:\n%s", out)
	}

	out = captureStdout(t, func() { rc = cmdUpdate([]string{"task", "mid", "--remove-tag", "driver"}) })
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(out, "no longer a driver") {
		t.Errorf("removing the driver tag should say so:\n%s", out)
	}
}

func TestUpdateTagDriverOnOtherTaskPrintsNoBlock(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	seedTask(t, "other")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "")
	out := captureStdout(t, func() { cmdUpdate([]string{"task", "other", "--tag", "driver"}) })
	if strings.Contains(out, driverRoleMarker) {
		t.Errorf("tagging a task this session isn't bound to must not inject the block:\n%s", out)
	}
}

func TestPromptAnchorMarksDriver(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	seedTask(t, "drv")
	bindSession(t, "drv", "sid-drv")
	anchor := func() string {
		return captureStdout(t, func() { cmdHookUserPromptSubmit(nil) })
	}
	if strings.Contains(anchor(), "[driver]") {
		t.Error("regular task anchor must not say [driver]")
	}
	tagTask(t, "drv", "driver")
	if !strings.Contains(anchor(), "[driver]") {
		t.Error("driver task anchor should say [driver]")
	}
}

func TestListShowsDriver(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "drv")
	seedTask(t, "plain")
	tagTask(t, "drv", "driver")

	out := captureStdout(t, func() { listTasksCmd([]string{"--format", "json"}) })
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	got := map[string]bool{}
	for _, r := range rows {
		d, _ := r["driver"].(bool)
		got[r["slug"].(string)] = d
	}
	if !got["drv"] || got["plain"] {
		t.Errorf("driver flags = %v, want drv=true plain=false", got)
	}

	table := captureStdout(t, func() { listTasksCmd([]string{"--no-color"}) })
	if !strings.Contains(table, "◆driver") {
		t.Errorf("table should mark driver tasks with ◆driver:\n%s", table)
	}
	if strings.Contains(table, "#driver") {
		t.Errorf("driver tag should render as the marker, not #driver:\n%s", table)
	}
}

func TestShowTaskPrintsDriverRole(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "drv")
	tagTask(t, "drv", "driver")
	out := captureStdout(t, func() { cmdShow([]string{"task", "drv"}) })
	if !strings.Contains(out, "role:") || !strings.Contains(out, "driver") {
		t.Errorf("show task should print role: driver:\n%s", out)
	}
}

// A crashed child (auto run finalized dead) must tell its driver — a dead
// child otherwise goes silent and the driver never hears about it.
func TestDeadChildNotifiesParent(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "parent")
	seedTask(t, "child")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET parent_slug='parent' WHERE slug='child'`); err != nil {
		t.Fatal(err)
	}
	seedRunningAuto(t, "child", 4242)
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		return nil // exits cleanly but never marked itself done → dead
	})
	cmdAutoExec([]string{"child"})

	msgs, err := pendingForIdentity(openFlowDB(t), busSender{Assignee: busUser, TaskSlug: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("parent inbox has %d messages, want 1", len(msgs))
	}
	if b := msgs[0].Body; !strings.Contains(b, "child") || !strings.Contains(b, "dead") {
		t.Errorf("message body %q should name the child and say dead", b)
	}
}

func TestCompletedChildSendsNoCrashNotice(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "parent")
	seedTask(t, "child")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET parent_slug='parent' WHERE slug='child'`); err != nil {
		t.Fatal(err)
	}
	seedRunningAuto(t, "child", 4242)
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		_, err := openFlowDB(t).Exec(`UPDATE tasks SET status='done' WHERE slug='child'`)
		return err
	})
	cmdAutoExec([]string{"child"})
	msgs, _ := pendingForIdentity(openFlowDB(t), busSender{Assignee: busUser, TaskSlug: "parent"})
	if len(msgs) != 0 {
		t.Errorf("completed child should not post a crash notice; got %d", len(msgs))
	}
}

// The intake driver question must be spelled out in the resident core:
// a pointer to a reference file alone gets skipped in practice.
func TestSkillCoreIntakeAsksDriverQuestion(t *testing.T) {
	core, err := fs.ReadFile(skillFiles(), "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(core)
	for _, want := range []string{"How should this task run?", "--tag driver", "Driver (Recommended)"} {
		if !strings.Contains(s, want) {
			t.Errorf("SKILL.md intake missing %q", want)
		}
	}
}
