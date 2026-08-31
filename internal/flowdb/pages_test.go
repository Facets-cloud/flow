package flowdb

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func openPagesTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestPageMessageLifecycle(t *testing.T) {
	db := openPagesTestDB(t)

	m := &PageMessage{
		ID: "aaaa0001", CreatedAt: NowISO(), Kind: "page",
		FromAssignee: "self", FromTaskSlug: "task-a", SenderSessionID: "sid-1",
		ToAssignee: "self", Body: "need approval", Urgent: true,
		NextNotifyAt: time.Now().Add(-time.Second).UTC().Format(time.RFC3339),
	}
	if err := InsertPageMessage(db, m); err != nil {
		t.Fatalf("insert: %v", err)
	}

	pending, err := PendingHumanPages(db, "self")
	if err != nil || len(pending) != 1 {
		t.Fatalf("PendingHumanPages = %v, %v; want 1 row", pending, err)
	}
	if !pending[0].Urgent || pending[0].FromTaskSlug != "task-a" {
		t.Errorf("row roundtrip mismatch: %+v", pending[0])
	}

	due, err := DueHumanPages(db, time.Now())
	if err != nil || len(due) != 1 {
		t.Fatalf("DueHumanPages = %v, %v; want 1", due, err)
	}
	if err := BumpNotifyAttempt(db, m.ID, 0, time.Now()); err != nil {
		t.Fatalf("bump: %v", err)
	}
	if due, _ = DueHumanPages(db, time.Now()); len(due) != 0 {
		t.Errorf("after bump, page should not be due yet")
	}

	acked, err := AckHumanPagesFromSession(db, "sid-1", "prompt")
	if err != nil || len(acked) != 1 {
		t.Fatalf("AckHumanPagesFromSession = %v, %v; want 1", acked, err)
	}
	if pending, _ = PendingHumanPages(db, "self"); len(pending) != 0 {
		t.Errorf("acked page still pending")
	}
	s, err := GetPageStats(db, "self")
	if err != nil || s.Acked != 1 || s.Pending != 0 {
		t.Errorf("stats = %+v, %v; want acked=1 pending=0", s, err)
	}
}

func TestTaskInboxDeliver(t *testing.T) {
	db := openPagesTestDB(t)
	m := &PageMessage{
		ID: "bbbb0001", CreatedAt: NowISO(), Kind: "post",
		FromAssignee: "self", FromTaskSlug: "task-a",
		ToAssignee: "self", ToTaskSlug: "task-b", Body: "fyi done",
	}
	if err := InsertPageMessage(db, m); err != nil {
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

func TestWatchesAndFanoutTargets(t *testing.T) {
	db := openPagesTestDB(t)
	for _, w := range [][2]string{
		{"self/task-b", "task-a"},
		{"self", "proj-x"},
		{"self/task-c", "someone-else"},
	} {
		if err := AddWatch(db, w[0], w[1]); err != nil {
			t.Fatalf("AddWatch(%v): %v", w, err)
		}
	}
	// duplicate subscribe is a no-op
	if err := AddWatch(db, "self/task-b", "task-a"); err != nil {
		t.Fatalf("dup AddWatch: %v", err)
	}

	got, err := WatchersOf(db, []string{"task-a", "proj-x", "self"})
	if err != nil {
		t.Fatalf("WatchersOf: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("WatchersOf = %v; want [self/task-b self]", got)
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

func TestPageEndpointsAndSweep(t *testing.T) {
	db := openPagesTestDB(t)
	if err := UpsertPageEndpoint(db, &PageEndpoint{TaskSlug: "task-a", TTY: "/dev/ttys001", SessionID: "sid"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// second upsert with zero fields must preserve tty
	if err := UpsertPageEndpoint(db, &PageEndpoint{TaskSlug: "task-a", ListenPID: 42, ListenHeartbeat: NowISO()}); err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	ep, err := GetPageEndpoint(db, "task-a")
	if err != nil || ep == nil {
		t.Fatalf("get: %v %v", ep, err)
	}
	if ep.TTY != "/dev/ttys001" || ep.ListenPID != 42 {
		t.Errorf("endpoint merge lost fields: %+v", ep)
	}

	old := time.Now().AddDate(0, 0, -120).UTC().Format(time.RFC3339)
	_ = InsertPageMessage(db, &PageMessage{
		ID: "old00001", CreatedAt: old, Kind: "page",
		FromAssignee: "self", ToAssignee: "self", Body: "ancient",
	})
	if _, err := db.Exec(`UPDATE page_messages SET status='acked' WHERE id='old00001'`); err != nil {
		t.Fatal(err)
	}
	_ = InsertPageMessage(db, &PageMessage{
		ID: "old00002", CreatedAt: old, Kind: "page",
		FromAssignee: "self", ToAssignee: "self", Body: "ancient but pending",
	})
	if err := SweepPages(db, time.Now()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM page_messages WHERE id='old00001'`).Scan(&n)
	if n != 0 {
		t.Errorf("sweep kept old acked row")
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM page_messages WHERE id='old00002'`).Scan(&n)
	if n != 1 {
		t.Errorf("sweep deleted a PENDING row — pending pages must never expire")
	}
}
