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
	now := time.Now()
	if err := BumpNotifyAttempt(db, m.ID, 0, now); err != nil {
		t.Fatalf("bump: %v", err)
	}
	if due, _ = DueBusMessages(db, "self", now); len(due) != 0 {
		t.Errorf("after bump, message should not be due yet")
	}
	// Off-by-one guard: the first re-notify lands at base 1m, not 2m.
	if due, _ = DueBusMessages(db, "self", now.Add(61*time.Second)); len(due) != 1 {
		t.Errorf("first backoff step should be 1m")
	}
	// Overflow guard: absurd attempts still cap at 30m, never negative.
	if err := BumpNotifyAttempt(db, m.ID, 1000000, now); err != nil {
		t.Fatalf("bump big: %v", err)
	}
	if due, _ = DueBusMessages(db, "self", now.Add(29*time.Minute)); len(due) != 0 {
		t.Errorf("capped delay should not be due before 30m")
	}
	if due, _ = DueBusMessages(db, "self", now.Add(31*time.Minute)); len(due) != 1 {
		t.Errorf("capped delay must be due after 30m (overflow would push it to never/always)")
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
		ID: "bbbb0001", CreatedAt: NowISO(), Kind: "broadcast",
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
	// Pending question to the human must survive close-out.
	if rows, _ := PendingForHuman(db, "self"); len(rows) != 1 || rows[0].ID != "fr000001" {
		t.Errorf("human-directed pending message did not survive: %v", rows)
	}
	if ws, _ := ListWatches(db, "self/task-y"); len(ws) != 1 {
		t.Errorf("unrelated watch was deleted")
	}
}

func TestBusSweepRollsConsumedByCount(t *testing.T) {
	db := openBusTestDB(t)
	// 5 consumed rows + 1 pending; a keep-window larger than 5 keeps all.
	for i := 0; i < 5; i++ {
		id := NowISO() + string(rune('a'+i))
		_ = InsertBusMessage(db, &BusMessage{
			ID: id, CreatedAt: NowISO(), Kind: "message",
			FromAssignee: "self", ToAssignee: "self", Body: "old"})
		if _, err := db.Exec(`UPDATE bus_messages SET status='acked' WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	_ = InsertBusMessage(db, &BusMessage{
		ID: "pend0001", CreatedAt: NowISO(), Kind: "message",
		FromAssignee: "self", ToAssignee: "self", Body: "pending forever"})
	// A pending BROADCAST is rollable (FYI, not a debt) — unlike the
	// pending directed message above.
	_ = InsertBusMessage(db, &BusMessage{
		ID: "bcst0001", CreatedAt: NowISO(), Kind: "broadcast",
		FromAssignee: "self", ToAssignee: "self", Body: "unread fyi"})

	if err := SweepBus(db, time.Now()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	var rollable, immortal int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_messages WHERE status != 'pending' OR kind='broadcast'`).Scan(&rollable)
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_messages WHERE status='pending' AND kind='message'`).Scan(&immortal)
	if rollable != 6 || immortal != 1 {
		t.Errorf("within window: rollable=%d immortal=%d", rollable, immortal)
	}

	// Shrink the window via the same rolling delete to prove only the
	// newest N rollable rows survive and the pending directed message
	// never expires.
	if _, err := db.Exec(`DELETE FROM bus_messages WHERE (status != 'pending' OR kind='broadcast')
        AND id NOT IN (SELECT id FROM bus_messages WHERE (status != 'pending' OR kind='broadcast')
                       ORDER BY created_at DESC LIMIT 2)`); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_messages WHERE status != 'pending' OR kind='broadcast'`).Scan(&rollable)
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_messages WHERE status='pending' AND kind='message'`).Scan(&immortal)
	if rollable != 2 || immortal != 1 {
		t.Errorf("after roll: rollable=%d immortal=%d — pending messages must never expire", rollable, immortal)
	}
}
