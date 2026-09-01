package flowdb

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func openBusTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestBusMessageLifecycle(t *testing.T) {
	db := openBusTestDB(t)

	m := &BusMessage{
		ID: "aaaa0001", CreatedAt: NowISO(), Kind: "message",
		FromAssignee: "self", FromTaskSlug: "task-a", SenderSessionID: "sid-1",
		ToAssignee: "self", Body: "need approval", Urgent: true,
		NextNotifyAt: time.Now().Add(-time.Second).UTC().Format(time.RFC3339),
	}
	if err := InsertBusMessage(db, m); err != nil {
		t.Fatalf("insert: %v", err)
	}

	pending, err := PendingForHuman(db, "self")
	if err != nil || len(pending) != 1 {
		t.Fatalf("PendingForHuman = %v, %v; want 1 row", pending, err)
	}
	if !pending[0].Urgent || pending[0].FromTaskSlug != "task-a" {
		t.Errorf("row roundtrip mismatch: %+v", pending[0])
	}

	due, err := DueBusMessages(db, "self", time.Now())
	if err != nil || len(due) != 1 {
		t.Fatalf("DueBusMessages = %v, %v; want 1", due, err)
	}
	if err := BumpNotifyAttempt(db, m.ID, 0, time.Now()); err != nil {
		t.Fatalf("bump: %v", err)
	}
	if due, _ = DueBusMessages(db, "self", time.Now()); len(due) != 0 {
		t.Errorf("after bump, message should not be due yet")
	}

	acked, err := AckHumanMessagesFromSession(db, "sid-1", "prompt")
	if err != nil || len(acked) != 1 {
		t.Fatalf("AckHumanMessagesFromSession = %v, %v; want 1", acked, err)
	}
	if pending, _ = PendingForHuman(db, "self"); len(pending) != 0 {
		t.Errorf("acked message still pending")
	}
	s, err := GetBusStats(db, "self")
	if err != nil || s.Acked != 1 || s.Pending != 0 {
		t.Errorf("stats = %+v, %v; want acked=1 pending=0", s, err)
	}
}

func TestBusTaskInboxDeliver(t *testing.T) {
	db := openBusTestDB(t)
	if err := InsertBusMessage(db, &BusMessage{
		ID: "bbbb0001", CreatedAt: NowISO(), Kind: "post",
		FromAssignee: "self", FromTaskSlug: "task-a",
		ToAssignee: "self", ToTaskSlug: "task-b", Body: "fyi done",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	rows, err := PendingForTask(db, "task-b")
	if err != nil || len(rows) != 1 {
		t.Fatalf("PendingForTask = %v, %v; want 1", rows, err)
	}
	if err := MarkDelivered(db, []string{"bbbb0001"}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if rows, _ = PendingForTask(db, "task-b"); len(rows) != 0 {
		t.Errorf("delivered row still pending")
	}
}

func TestBusWatches(t *testing.T) {
	db := openBusTestDB(t)
	for _, w := range [][2]string{
		{"self/task-b", "task-a"},
		{"self", "proj-x"},
		{"self/task-c", "someone-else"},
	} {
		if err := AddWatch(db, w[0], w[1]); err != nil {
			t.Fatalf("AddWatch(%v): %v", w, err)
		}
	}
	if err := AddWatch(db, "self/task-b", "task-a"); err != nil { // dup: no-op
		t.Fatalf("dup AddWatch: %v", err)
	}
	got, err := WatchersOf(db, []string{"task-a", "proj-x", "self"})
	if err != nil || len(got) != 2 {
		t.Fatalf("WatchersOf = %v, %v; want 2 watchers", got, err)
	}
	ws, _ := ListWatches(db, "self/task-b")
	if len(ws) != 1 || ws[0] != "task-a" {
		t.Errorf("ListWatches = %v", ws)
	}
	removed, err := RemoveWatch(db, "self/task-b", "task-a")
	if err != nil || !removed {
		t.Errorf("RemoveWatch = %v, %v", removed, err)
	}
}

func TestCleanupTaskBus(t *testing.T) {
	db := openBusTestDB(t)
	// Rows the cleanup must remove:
	_ = InsertBusMessage(db, &BusMessage{ID: "to000001", CreatedAt: NowISO(), Kind: "message",
		FromAssignee: "self", ToAssignee: "self", ToTaskSlug: "task-x", Body: "undeliverable"})
	_ = AddWatch(db, "self/task-x", "other-task")
	_ = AddWatch(db, "self", "task-x")
	_ = RecordNudge(db, "task-x")
	_ = UpsertBusListener(db, "self/task-x", 42)
	// Rows the cleanup must keep:
	_ = InsertBusMessage(db, &BusMessage{ID: "fr000001", CreatedAt: NowISO(), Kind: "message",
		FromAssignee: "self", FromTaskSlug: "task-x", ToAssignee: "self", Body: "still asks the human"})
	_ = AddWatch(db, "self/task-y", "unrelated")

	if err := CleanupTaskBus(db, "task-x"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if rows, _ := PendingForTask(db, "task-x"); len(rows) != 0 {
		t.Errorf("undeliverable inbox rows survived")
	}
	if ws, _ := ListWatches(db, "self/task-x"); len(ws) != 0 {
		t.Errorf("watches BY closed task survived: %v", ws)
	}
	if got, _ := WatchersOf(db, []string{"task-x"}); len(got) != 0 {
		t.Errorf("watches ON closed task survived: %v", got)
	}
	if nudged, _, _ := GetNudgeState(db, "task-x"); nudged != "" {
		t.Errorf("nudge stamp survived")
	}
	if pid, _, _ := GetBusListener(db, "self/task-x"); pid != 0 {
		t.Errorf("listener survived")
	}
	// Pending question to the human must survive close-out.
	if rows, _ := PendingForHuman(db, "self"); len(rows) != 1 || rows[0].ID != "fr000001" {
		t.Errorf("human-directed pending message did not survive: %v", rows)
	}
	if ws, _ := ListWatches(db, "self/task-y"); len(ws) != 1 {
		t.Errorf("unrelated watch was deleted")
	}
}

func TestBusListenersAndSweep(t *testing.T) {
	db := openBusTestDB(t)
	if err := UpsertBusListener(db, "self/task-a", 42); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	pid, hb, err := GetBusListener(db, "self/task-a")
	if err != nil || pid != 42 || hb == "" {
		t.Fatalf("GetBusListener = %d, %q, %v", pid, hb, err)
	}
	if err := RemoveBusListener(db, "self/task-a", 42); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if pid, _, _ = GetBusListener(db, "self/task-a"); pid != 0 {
		t.Errorf("listener not removed")
	}

	old := time.Now().AddDate(0, 0, -120).UTC().Format(time.RFC3339)
	_ = InsertBusMessage(db, &BusMessage{
		ID: "old00001", CreatedAt: old, Kind: "message",
		FromAssignee: "self", ToAssignee: "self", Body: "ancient",
	})
	if _, err := db.Exec(`UPDATE bus_messages SET status='acked' WHERE id='old00001'`); err != nil {
		t.Fatal(err)
	}
	_ = InsertBusMessage(db, &BusMessage{
		ID: "old00002", CreatedAt: old, Kind: "message",
		FromAssignee: "self", ToAssignee: "self", Body: "ancient but pending",
	})
	if err := SweepBus(db, time.Now()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_messages WHERE id='old00001'`).Scan(&n)
	if n != 0 {
		t.Errorf("sweep kept old acked row")
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_messages WHERE id='old00002'`).Scan(&n)
	if n != 1 {
		t.Errorf("sweep deleted a PENDING row — pending messages must never expire")
	}
}
