package app

import (
	"testing"
	"time"

	"flow/internal/flowdb"
)

// CodeRabbit (PR #168): a second child report stamped in the revive's start
// second was skipped with the trigger. With the trigger id known, only that
// message is skipped.
func TestReviveRecheckSameSecondReportAfterTrigger(t *testing.T) {
	db, envs := reviveSetup(t)
	now := time.Now().Format(time.RFC3339)
	for _, id := range []string{"trig", "late"} {
		m := &flowdb.BusMessage{ID: id, CreatedAt: now, Kind: "message",
			FromAssignee: "user", FromTaskSlug: "c", ToAssignee: "user", ToTaskSlug: "d", Body: "x"}
		oldAfter := flowdb.AfterInsert
		flowdb.AfterInsert = nil
		err := flowdb.InsertBusMessage(db, m)
		flowdb.AfterInsert = oldAfter
		if err != nil {
			t.Fatal(err)
		}
		_ = flowdb.MarkChildReport(db, "d", id)
	}
	t.Setenv(reviveTriggerEnv, "trig")
	if !reviveIfMailLanded(db, "d", now) {
		t.Fatal("same-second report after the trigger was not picked up")
	}
	if len(*envs) != 1 {
		t.Fatalf("launches = %d, want 1", len(*envs))
	}
	if !hasEnv((*envs)[0], reviveTriggerEnv+"=late") {
		t.Errorf("new run's trigger not set to the late report: %v", (*envs)[0])
	}
}

// A completed driver (flow done inside the revive) leaves no revive FYI
// saying its mail is waiting.
func TestFinishReviveCompletedRetractsStaleFYIs(t *testing.T) {
	db, _ := reviveSetup(t)
	fyi := &flowdb.BusMessage{ID: "f1", CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: busUser, FromTaskSlug: "d", ToAssignee: busUser, FYI: true,
		Body: reviveRefusedPrefix + "d for its child's report: rate limit."}
	oldAfter := flowdb.AfterInsert
	flowdb.AfterInsert = nil
	err := flowdb.InsertBusMessage(db, fyi)
	flowdb.AfterInsert = oldAfter
	if err != nil {
		t.Fatal(err)
	}
	task, err := flowdb.GetTask(db, "d")
	if err != nil || task == nil {
		t.Fatal(err)
	}
	finishRevive(db, task, "completed", flowdb.NowISO())
	ms, err := flowdb.UnreadForHuman(db, busUser)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.ID == "f1" {
			t.Error("stale revive FYI still unread after the driver completed")
		}
	}
}
