package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness"
)

// seedStartedTask creates a task that already has a session (so done and
// status flips are legal), optionally under a parent.
func seedStartedTask(t *testing.T, slug, parent string) {
	t.Helper()
	seedTask(t, slug)
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET status='in-progress', session_id=?, session_started=?, parent_slug=? WHERE slug=?`,
		fakeSessionID(slug), flowdb.NowISO(), flowdb.NullIfEmpty(parent), slug,
	); err != nil {
		t.Fatal(err)
	}
}

func taskEvents(t *testing.T, slug string, kinds ...string) []*flowdb.TaskEvent {
	t.Helper()
	evs, err := flowdb.ListTaskEvents(openFlowDB(t), flowdb.TaskEventFilter{TaskSlug: slug, Kinds: kinds})
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func eventDetail(t *testing.T, e *flowdb.TaskEvent) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(e.Detail, &d); err != nil {
		t.Fatalf("detail %s: %v", e.Detail, err)
	}
	return d
}

// TestDoneRecordsClosingActor pins who closed a task: the task's own auto
// run, its parent's driver session, another session, or a human.
func TestDoneRecordsClosingActor(t *testing.T) {
	cases := []struct {
		name      string
		autoRun   bool   // FLOW_AUTO_RUN = child slug
		sessionOf string // CLAUDE_CODE_SESSION_ID = this task's session ("" = none)
		want      string
	}{
		{"human", false, "", flowdb.ActorHuman},
		{"auto-run", true, "", flowdb.ActorAutoRun},
		{"driver", false, "parent", flowdb.ActorDriver},
		{"other-session", false, "other", flowdb.ActorSession},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupFlowRoot(t)
			stubClaudeRunner(t, nil)
			seedStartedTask(t, "parent", "")
			seedStartedTask(t, "other", "")
			seedStartedTask(t, "child", "parent")
			t.Setenv("CLAUDE_CODE_SESSION_ID", "")
			if tc.sessionOf != "" {
				t.Setenv("CLAUDE_CODE_SESSION_ID", fakeSessionID(tc.sessionOf))
			}
			if tc.autoRun {
				t.Setenv(autoRunEnvVar, "child")
			}
			if rc := cmdDone([]string{"child"}); rc != 0 {
				t.Fatalf("done rc=%d", rc)
			}
			evs := taskEvents(t, "child", flowdb.EventDone)
			if len(evs) != 1 {
				t.Fatalf("done events = %d, want 1", len(evs))
			}
			if evs[0].Actor != tc.want {
				t.Errorf("actor = %q, want %q", evs[0].Actor, tc.want)
			}
			if got := eventDetail(t, evs[0])["from"]; got != "in-progress" {
				t.Errorf("from = %v, want in-progress", got)
			}
		})
	}
}

// TestDoneTwiceRecordsOneEvent: the idempotent second done records nothing.
func TestDoneTwiceRecordsOneEvent(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	seedStartedTask(t, "t1", "")
	cmdDone([]string{"t1"})
	cmdDone([]string{"t1"})
	if n := len(taskEvents(t, "t1", flowdb.EventDone)); n != 1 {
		t.Errorf("done events = %d, want 1", n)
	}
}

// TestUpdateStatusOffDoneIsReopen: leaving done is recorded as a reopen;
// other changes are plain status events; a no-op change records nothing.
func TestUpdateStatusOffDoneIsReopen(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	seedStartedTask(t, "t1", "")
	cmdDone([]string{"t1"})
	if rc := cmdUpdate([]string{"task", "t1", "--status", "in-progress"}); rc != 0 {
		t.Fatalf("update rc=%d", rc)
	}
	if rc := cmdUpdate([]string{"task", "t1", "--status", "in-progress"}); rc != 0 {
		t.Fatalf("update rc=%d", rc)
	}
	reopens := taskEvents(t, "t1", flowdb.EventReopen)
	if len(reopens) != 1 {
		t.Fatalf("reopen events = %d, want 1", len(reopens))
	}
	if d := eventDetail(t, reopens[0]); d["from"] != "done" || d["to"] != "in-progress" {
		t.Errorf("reopen detail = %v", d)
	}
	if n := len(taskEvents(t, "t1", flowdb.EventStatus)); n != 0 {
		t.Errorf("status events = %d, want 0 (the no-op update records nothing)", n)
	}
}

// TestAutoExecRecordsRunFinished: every terminal auto-run status lands in
// the log with the supervisor as actor.
func TestAutoExecRecordsRunFinished(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	seedTask(t, "auto-task")
	seedRunningAuto(t, "auto-task", 4242)
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		return errors.New("harness exploded")
	})
	cmdAutoExec([]string{"auto-task"})
	evs := taskEvents(t, "auto-task", flowdb.EventRunFinished)
	if len(evs) != 1 {
		t.Fatalf("run-finished events = %d, want 1", len(evs))
	}
	if evs[0].Actor != flowdb.ActorSupervisor {
		t.Errorf("actor = %q, want supervisor", evs[0].Actor)
	}
	if d := eventDetail(t, evs[0]); d["status"] != "dead" {
		t.Errorf("status = %v, want dead", d["status"])
	}
}

// TestAutoExecFinalizesBlocked: a run that sets waiting_on and exits
// without done did what the brief asks — it is 'blocked', not 'dead', and
// its parent is told what it waits on instead of "ended dead".
func TestAutoExecFinalizesBlocked(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	seedStartedTask(t, "driver", "")
	seedTask(t, "auto-task")
	seedRunningAuto(t, "auto-task", 4242)
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET parent_slug='driver' WHERE slug='auto-task'`); err != nil {
		t.Fatal(err)
	}
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		_, err := openFlowDB(t).Exec(`UPDATE tasks SET waiting_on='AWS account id' WHERE slug='auto-task'`)
		return err
	})
	if rc := cmdAutoExec([]string{"auto-task"}); rc != 0 {
		t.Fatalf("cmdAutoExec rc=%d, want 0", rc)
	}
	task, err := flowdb.GetTask(openFlowDB(t), "auto-task")
	if err != nil {
		t.Fatal(err)
	}
	if task.AutoRunStatus.String != "blocked" {
		t.Errorf("auto_run_status = %q, want blocked", task.AutoRunStatus.String)
	}
	evs := taskEvents(t, "auto-task", flowdb.EventRunFinished)
	if len(evs) != 1 || eventDetail(t, evs[0])["waiting_on"] != "AWS account id" {
		t.Fatalf("run-finished = %+v, want one with waiting_on", evs)
	}
	var body string
	if err := openFlowDB(t).QueryRow(
		`SELECT body FROM bus_messages WHERE from_task_slug='auto-task' AND to_task_slug='driver'`,
	).Scan(&body); err != nil {
		t.Fatalf("parent notice: %v", err)
	}
	if !strings.Contains(body, "blocked on: AWS account id") || strings.Contains(body, "dead") {
		t.Errorf("parent notice = %q, want a blocked notice", body)
	}
}

// TestAutoExecStaleWaitingStaysDead: a waiting_on left over from before
// the run does not make a crashed run look blocked.
func TestAutoExecStaleWaitingStaysDead(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "auto-task")
	seedRunningAuto(t, "auto-task", 4242)
	if _, err := openFlowDB(t).Exec(`UPDATE tasks SET waiting_on='old ask' WHERE slug='auto-task'`); err != nil {
		t.Fatal(err)
	}
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		return errors.New("harness exploded")
	})
	cmdAutoExec([]string{"auto-task"})
	task, err := flowdb.GetTask(openFlowDB(t), "auto-task")
	if err != nil {
		t.Fatal(err)
	}
	if task.AutoRunStatus.String != "dead" {
		t.Errorf("auto_run_status = %q, want dead", task.AutoRunStatus.String)
	}
}

// TestCmdEventsJSON pins the read path consumers use: a JSON array,
// filterable by task and kind, with an incremental id cursor.
func TestCmdEventsJSON(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	seedStartedTask(t, "t1", "")
	seedStartedTask(t, "t2", "")
	cmdDone([]string{"t1"})
	cmdDone([]string{"t2"})

	var all []flowdb.TaskEvent
	out := captureStdout(t, func() {
		if rc := cmdEvents([]string{"--format", "json"}); rc != 0 {
			t.Fatalf("events rc=%d", rc)
		}
	})
	if err := json.Unmarshal([]byte(out), &all); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(all) != 2 || all[0].TaskSlug != "t1" || all[1].Kind != flowdb.EventDone {
		t.Fatalf("events = %+v", all)
	}

	var one []flowdb.TaskEvent
	out = captureStdout(t, func() { cmdEvents([]string{"t2", "--format", "json"}) })
	if err := json.Unmarshal([]byte(out), &one); err != nil || len(one) != 1 || one[0].TaskSlug != "t2" {
		t.Fatalf("t2 events = %s (%v)", out, err)
	}

	var after []flowdb.TaskEvent
	out = captureStdout(t, func() {
		cmdEvents([]string{"--after-id", jsonInt(all[1].ID), "--format", "json"})
	})
	if err := json.Unmarshal([]byte(out), &after); err != nil || len(after) != 0 {
		t.Fatalf("after-id events = %s (%v), want []", out, err)
	}
}

func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }
