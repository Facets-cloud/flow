package flowdb

import (
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"
)

func wakeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenDB(t.TempDir() + "/flow.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

var wakeMsgSeq int

func putTaskMsg(t *testing.T, db *sql.DB, slug string) *BusMessage {
	t.Helper()
	wakeMsgSeq++
	m := &BusMessage{ID: fmt.Sprintf("w%04d", wakeMsgSeq), CreatedAt: NowISO(), Kind: "message",
		FromAssignee: "user", ToAssignee: "user", ToTaskSlug: slug, Body: "hi"}
	if err := InsertBusMessage(db, m); err != nil {
		t.Fatal(err)
	}
	return m
}

func ids(ms []*BusMessage) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

// The atomic claim: whichever path's INSERT OR IGNORE wins announces;
// every later claimer gets nothing for the same rows.
func TestClaimUnsurfacedOnce(t *testing.T) {
	db := wakeTestDB(t)
	if got, err := ClaimUnsurfaced(db, "a"); err != nil || len(got) != 0 {
		t.Fatalf("empty inbox: %v %v", got, err)
	}
	m1 := putTaskMsg(t, db, "a")
	m2 := putTaskMsg(t, db, "a")
	got, err := ClaimUnsurfaced(db, "a")
	if err != nil || len(got) != 2 || got[0].ID != m1.ID || got[1].ID != m2.ID {
		t.Fatalf("first claim = %v %v", ids(got), err)
	}
	if again, _ := ClaimUnsurfaced(db, "a"); len(again) != 0 {
		t.Fatalf("second claim re-announced: %v", ids(again))
	}
	m3 := putTaskMsg(t, db, "a")
	if got, _ := ClaimUnsurfaced(db, "a"); len(got) != 1 || got[0].ID != m3.ID {
		t.Fatalf("only the new row: %v", ids(got))
	}
	// Other tasks are independent; consumed rows never claim.
	m4 := putTaskMsg(t, db, "b")
	if ok, _ := ClaimDelivered(db, m4.ID, Consumer{By: "pop"}); !ok {
		t.Fatal("pop")
	}
	if got, _ := ClaimUnsurfaced(db, "b"); len(got) != 0 {
		t.Fatalf("consumed row claimed: %v", ids(got))
	}
}

// Concurrent claimers (sender wake, Stop check, PreToolUse, waiter) never
// both win the same row.
func TestClaimUnsurfacedConcurrent(t *testing.T) {
	db := wakeTestDB(t)
	for i := 0; i < 20; i++ {
		putTaskMsg(t, db, "a")
	}
	var mu sync.Mutex
	won := map[string]int{}
	var wg sync.WaitGroup
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := ClaimUnsurfaced(db, "a")
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			for _, m := range got {
				won[m.ID]++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(won) != 20 {
		t.Fatalf("claimed %d of 20 rows", len(won))
	}
	for id, n := range won {
		if n != 1 {
			t.Fatalf("row %s claimed %d times", id, n)
		}
	}
}

// A push that failed releases its claim so a fallback can announce.
func TestReleaseSurfaced(t *testing.T) {
	db := wakeTestDB(t)
	putTaskMsg(t, db, "a")
	got, _ := ClaimUnsurfaced(db, "a")
	if err := ReleaseSurfaced(db, "a", ids(got)); err != nil {
		t.Fatal(err)
	}
	if again, _ := ClaimUnsurfaced(db, "a"); len(again) != 1 {
		t.Fatalf("released row not claimable: %v", ids(again))
	}
}

// ClaimUnsurfacedOlderThan leaves young rows for the primary push path.
func TestClaimUnsurfacedOlderThan(t *testing.T) {
	db := wakeTestDB(t)
	old := &BusMessage{ID: "old1", CreatedAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), Kind: "message",
		FromAssignee: "user", ToAssignee: "user", ToTaskSlug: "a", Body: "x"}
	if err := InsertBusMessage(db, old); err != nil {
		t.Fatal(err)
	}
	putTaskMsg(t, db, "a") // brand new
	got, err := ClaimUnsurfacedOlderThan(db, "a", time.Now().Add(-5*time.Second))
	if err != nil || len(got) != 1 || got[0].ID != "old1" {
		t.Fatalf("got %v %v", ids(got), err)
	}
}

func TestSessionState(t *testing.T) {
	db := wakeTestDB(t)
	st, err := GetSessionState(db, "a")
	if err != nil || st.State != "" {
		t.Fatalf("unknown task: %+v %v", st, err)
	}
	t0 := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	if err := SetSessionState(db, "a", SessionBusy, t0); err != nil {
		t.Fatal(err)
	}
	st, _ = GetSessionState(db, "a")
	if st.State != SessionBusy || !st.LastActivity.Equal(t0) {
		t.Fatalf("busy: %+v", st)
	}
	if err := TouchSessionActivity(db, "a", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	st, _ = GetSessionState(db, "a")
	if st.State != SessionBusy || !st.LastActivity.Equal(t0.Add(time.Minute)) {
		t.Fatalf("touch: %+v", st)
	}
	_ = SetSessionState(db, "a", SessionIdle, t0.Add(2*time.Minute))
	st, _ = GetSessionState(db, "a")
	if st.State != SessionIdle {
		t.Fatalf("idle: %+v", st)
	}
	// Stale busy reads as idle.
	_ = SetSessionState(db, "a", SessionBusy, t0)
	st, _ = GetSessionState(db, "a")
	if !st.Idle(t0.Add(11*time.Minute), 10*time.Minute) || st.Idle(t0.Add(time.Minute), 10*time.Minute) {
		t.Fatalf("stale-busy math wrong: %+v", st)
	}
	// Unknown state counts as idle (a session flow never saw a hook from).
	if !(SessionStateRow{}).Idle(t0, time.Minute) {
		t.Fatal("unknown state must read idle")
	}
}

func TestAfterInsertCallback(t *testing.T) {
	db := wakeTestDB(t)
	var got []string
	old := AfterInsert
	AfterInsert = func(_ *sql.DB, m *BusMessage) { got = append(got, m.ID) }
	defer func() { AfterInsert = old }()

	m := putTaskMsg(t, db, "a")
	if len(got) != 1 || got[0] != m.ID {
		t.Fatalf("callback after insert: %v", got)
	}
	// A connector-relayed answer inserts the reply inside a tx: the
	// callback must fire for it too (after commit).
	ask := &BusMessage{ID: "ask1", CreatedAt: NowISO(), Kind: "message", FromAssignee: "user",
		FromTaskSlug: "a", ToAssignee: BusUser, Body: "ok?"}
	if err := InsertBusMessage(db, ask); err != nil {
		t.Fatal(err)
	}
	reply := &BusMessage{ID: "rep1", CreatedAt: NowISO(), Kind: "message", FromAssignee: "user",
		ToAssignee: "user", ToTaskSlug: "a", Body: "yes", ReplyTo: "ask1"}
	if _, err := AnswerHumanMessage(db, "ask1", Consumer{By: "connector"}, reply); err != nil {
		t.Fatal(err)
	}
	if got[len(got)-1] != "rep1" {
		t.Fatalf("reply did not trigger callback: %v", got)
	}
}

func TestCleanupTaskBusDropsSessionState(t *testing.T) {
	db := wakeTestDB(t)
	_ = SetSessionState(db, "a", SessionIdle, time.Now())
	if err := CleanupTaskBus(db, "a"); err != nil {
		t.Fatal(err)
	}
	if st, _ := GetSessionState(db, "a"); st.State != "" {
		t.Fatalf("session state survived cleanup: %+v", st)
	}
}
