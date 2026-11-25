package app

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness"
)

// driverRoleMarker is the heading of the stronger block a task tagged
// `driver` gets. Regular bound tasks keep the light delegation block
// (delegationMarker); tests key on the markers so wording can evolve.
const driverRoleMarker = "Reminder: you are the DRIVER for this task. Read driver.md ("

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
	// The rules live in the skill's references/driver.md; the hook only
	// points at the installed copy.
	if !strings.Contains(out, "driver.md") {
		t.Errorf("driver pointer must name driver.md:\n%s", out)
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
		// Every human-driven bound session is a driver; no tag needed.
		{"untagged", driverRoleMarker, "", false},
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

// Every interactive session is a driver whatever its tags, so tagging or
// untagging `driver` announces no role change.
func TestUpdateTagDriverAnnouncesNoRoleChange(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	seedTask(t, "mid")
	bindSession(t, "mid", "sid-mid")
	for _, args := range [][]string{{"--tag", "driver"}, {"--remove-tag", "driver"}} {
		out := captureStdout(t, func() {
			if rc := cmdUpdate(append([]string{"task", "mid"}, args...)); rc != 0 {
				t.Fatalf("rc=%d", rc)
			}
		})
		if strings.Contains(out, driverRoleMarker) || strings.Contains(out, "flow DRIVER task") || strings.Contains(out, "no longer a driver") {
			t.Errorf("%v must not announce a role change:\n%s", args, out)
		}
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

// Children get a background inbox Monitor from the SessionStart hook, so
// brief guidance must say "don't block", not "don't wait on your inbox":
// the old phrasing read as "skip the Monitor".
func TestNoBlockWordingIsConsistent(t *testing.T) {
	ref, err := fs.ReadFile(skillFiles(), "references/delegation.md")
	if err != nil {
		t.Fatal(err)
	}
	for where, s := range map[string]string{
		"delegation.md": string(ref),
		"light block":   driverDelegationBlock(),
		"driver block":  driverRoleBlock(),
	} {
		if strings.Contains(s, "wait on its own inbox") || strings.Contains(s, "wait on your own inbox") {
			t.Errorf("%s still says not to wait on the inbox", where)
		}
		if !strings.Contains(s, "never block waiting for a reply") {
			t.Errorf("%s missing \"never block waiting for a reply\"", where)
		}
	}
	// The template brief is for an auto child, which arms no listener
	// (autoRunNoListenerRule): it must say how mail reaches it instead.
	if !strings.Contains(string(ref), "Arm no inbox listener") ||
		!strings.Contains(string(ref), "pop it in the foreground") {
		t.Error("delegation.md template should say the child arms no listener and pops in the foreground")
	}
}

// The driver line is a short reminder: it orders the driver.md read and
// states delegate-by-default on one line; auto runs and playbook runs
// still get nothing.
func TestDriverBlockOrdersReadAndDelegateByDefault(t *testing.T) {
	setupFlowRoot(t)
	clearSessionEnv(t)
	t.Setenv(autoRunEnvVar, "")
	seedTask(t, "core")
	writeTaskBriefDM(t, "core", agreedBrief)
	db := openFlowDB(t)
	tk, _ := flowdb.GetTask(db, "core")
	db.Close()
	blk := roleBlockFor(tk, false)
	if strings.Contains(strings.TrimSpace(blk), "\n") {
		t.Errorf("driver block must stay one line: %q", blk)
	}
	for _, want := range []string{
		driverRoleMarker,
		"driver.md) now to know the driver SOP.",
		"Default: self-contained work goes to a flow auto child, not the built-in subagent tool;",
		"keep it inline only when it is trivial single-step work, and say why.",
	} {
		if !strings.Contains(blk, want) {
			t.Errorf("driver block missing %q:\n%s", want, blk)
		}
	}
	if n := len(fmt.Sprintf(driverReminderFmt, "")); n > 600 {
		t.Errorf("driver reminder is %d chars without the path; keep it under 600 (printed every start/resume)", n)
	}

	pb := *tk
	pb.Kind = "playbook_run"
	if got := roleBlockFor(&pb, false); got != "" {
		t.Errorf("playbook run must get no driver block, got %q", got)
	}
	t.Setenv(autoRunEnvVar, "core")
	if got := roleBlockFor(tk, true); got != "" {
		t.Errorf("auto run must get no driver block, got %q", got)
	}
}
