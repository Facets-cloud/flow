package app

import (
	"testing"

	"flow/internal/flowdb"
)

// TestReviewAcceptRecordsDriverVerdict: a driver's accept is a review
// event attributed to the driver; the child's status is untouched.
func TestReviewAcceptRecordsDriverVerdict(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	seedStartedTask(t, "drv", "")
	seedStartedTask(t, "kid", "drv")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	cmdDone([]string{"kid"})

	t.Setenv("CLAUDE_CODE_SESSION_ID", fakeSessionID("drv"))
	if rc := cmdReview([]string{"kid", "accept", "--note", "tests green, PR #12"}); rc != 0 {
		t.Fatalf("review rc=%d", rc)
	}
	evs := taskEvents(t, "kid", flowdb.EventReview)
	if len(evs) != 1 || evs[0].Actor != flowdb.ActorDriver {
		t.Fatalf("review events = %+v, want one by the driver", evs)
	}
	if d := eventDetail(t, evs[0]); d["verdict"] != "accept" || d["note"] != "tests green, PR #12" {
		t.Errorf("detail = %v", d)
	}
	if task, _ := flowdb.GetTask(openFlowDB(t), "kid"); task.Status != "done" {
		t.Errorf("accept changed status to %q", task.Status)
	}
}

// TestReviewRejectReopensAndMessages: reject records the verdict, reopens
// the done child (a reopen event carrying the reason) and puts the reason
// in the child's inbox.
func TestReviewRejectReopensAndMessages(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	seedStartedTask(t, "drv", "")
	seedStartedTask(t, "kid", "drv")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	cmdDone([]string{"kid"})

	t.Setenv("CLAUDE_CODE_SESSION_ID", fakeSessionID("drv"))
	if rc := cmdReview([]string{"kid", "reject", "--reason", "migration not applied"}); rc != 0 {
		t.Fatalf("review rc=%d", rc)
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "kid")
	if task.Status != "in-progress" {
		t.Errorf("status = %q, want in-progress (reopened)", task.Status)
	}
	if evs := taskEvents(t, "kid", flowdb.EventReview); len(evs) != 1 || eventDetail(t, evs[0])["verdict"] != "reject" {
		t.Errorf("review events = %+v", evs)
	}
	reopens := taskEvents(t, "kid", flowdb.EventReopen)
	if len(reopens) != 1 || eventDetail(t, reopens[0])["reason"] != "review rejected: migration not applied" {
		t.Errorf("reopen events = %+v", reopens)
	}
	var body, from string
	if err := db.QueryRow(`SELECT body, from_task_slug FROM bus_messages WHERE to_task_slug='kid'`).Scan(&body, &from); err != nil {
		t.Fatalf("no message to the child: %v", err)
	}
	if from != "drv" || body != "review: your result was rejected — migration not applied" {
		t.Errorf("message from=%q body=%q", from, body)
	}
}

func TestReviewUsageErrors(t *testing.T) {
	setupFlowRoot(t)
	seedStartedTask(t, "kid", "")
	for _, args := range [][]string{
		{"kid"},
		{"kid", "maybe"},
		{"kid", "reject"},
		{"kid", "reject", "--reason", "  "},
	} {
		if rc := cmdReview(args); rc != 2 {
			t.Errorf("review %q rc=%d, want 2", args, rc)
		}
	}
	if n := len(taskEvents(t, "kid", flowdb.EventReview)); n != 0 {
		t.Errorf("usage errors recorded %d events", n)
	}
}
