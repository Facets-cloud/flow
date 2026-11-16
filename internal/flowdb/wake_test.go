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

// The two records are independent: a wake nudge never hides mail from
// the hooks, and a hook announcement never hides it from the sender's wake.
func TestWakeAndHookRecordsIndependent(t *testing.T) {
	db := wakeTestDB(t)
	m := putTaskMsg(t, db, "a")
	got, err := ClaimWakeNudge(db, "a", time.Time{}, false, time.Now())
	if err != nil || len(got) != 1 || got[0].ID != m.ID {
		t.Fatalf("wake claim = %v %v", ids(got), err)
	}
	// A swallowed push: the hooks still see the row as never shown.
	if h, _ := ClaimUnsurfaced(db, "a"); len(h) != 1 {
		t.Fatalf("wake nudge hid the row from the hooks: %v", ids(h))
	}
	// Coalescing: the same batch is never nudged twice.
	if again, _ := ClaimWakeNudge(db, "a", time.Time{}, false, time.Now()); len(again) != 0 {
		t.Fatalf("re-nudged the same rows: %v", ids(again))
	}
	// Hook first, then wake: the sender's wake is not blocked by it.
	m2 := putTaskMsg(t, db, "a")
	if h, _ := ClaimUnsurfaced(db, "a"); len(h) != 1 || h[0].ID != m2.ID {
		t.Fatalf("hook claim = %v", ids(h))
	}
	if w, _ := ClaimWakeNudge(db, "a", time.Time{}, false, time.Now()); len(w) != 1 || w[0].ID != m2.ID {
		t.Fatalf("hook record blocked the sender's wake: %v", ids(w))
	}
	// The waiter's form skips rows the hooks already showed in context.
	putTaskMsg(t, db, "a")
	_, _ = ClaimUnsurfaced(db, "a")
	if w, _ := ClaimWakeNudge(db, "a", time.Time{}, true, time.Now()); len(w) != 0 {
		t.Fatalf("waiter nudged hook-surfaced rows: %v", ids(w))
	}
}

// A push that failed releases the wake claim so a fallback can nudge.
func TestReleaseWakeNudge(t *testing.T) {
	db := wakeTestDB(t)
	putTaskMsg(t, db, "a")
	got, _ := ClaimWakeNudge(db, "a", time.Time{}, false, time.Now())
	if err := ReleaseWakeNudge(db, "a", ids(got)); err != nil {
		t.Fatal(err)
	}
	if again, _ := ClaimWakeNudge(db, "a", time.Time{}, false, time.Now()); len(again) != 1 {
		t.Fatalf("released row not claimable: %v", ids(again))
	}
}

// The cutoff leaves young rows for the sender's immediate push.
func TestClaimWakeNudgeOlderThan(t *testing.T) {
	db := wakeTestDB(t)
	old := &BusMessage{ID: "old1", CreatedAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), Kind: "message",
		FromAssignee: "user", ToAssignee: "user", ToTaskSlug: "a", Body: "x"}
	if err := InsertBusMessage(db, old); err != nil {
		t.Fatal(err)
	}
	putTaskMsg(t, db, "a") // brand new
	got, err := ClaimWakeNudge(db, "a", time.Now().Add(-5*time.Second), true, time.Now())
	if err != nil || len(got) != 1 || got[0].ID != "old1" {
		t.Fatalf("got %v %v", ids(got), err)
	}
}

// A nudged row still unread (and never shown by a hook) after the
// re-nudge window is re-nudged exactly once.
func TestClaimWakeRenudgeOncePerBatch(t *testing.T) {
	db := wakeTestDB(t)
	m := putTaskMsg(t, db, "a")
	t0 := time.Now().Add(-time.Minute)
	if got, _ := ClaimWakeNudge(db, "a", time.Time{}, false, t0); len(got) != 1 {
		t.Fatal("nudge")
	}
	// Too young: not yet.
	if got, _ := ClaimWakeRenudge(db, "a", t0.Add(-time.Second)); len(got) != 0 {
		t.Fatalf("re-nudged inside the window: %v", ids(got))
	}
	got, err := ClaimWakeRenudge(db, "a", time.Now().Add(-30*time.Second))
	if err != nil || len(got) != 1 || got[0].ID != m.ID {
		t.Fatalf("renudge = %v %v", ids(got), err)
	}
	if again, _ := ClaimWakeRenudge(db, "a", time.Now()); len(again) != 0 {
		t.Fatalf("second re-nudge: %v", ids(again))
	}
	// Rows a hook showed are never re-nudged; consumed rows neither.
	m2 := putTaskMsg(t, db, "a")
	_, _ = ClaimWakeNudge(db, "a", time.Time{}, false, t0)
	_ = MarkSurfaced(db, "a", []string{m2.ID})
	m3 := putTaskMsg(t, db, "a")
	_, _ = ClaimWakeNudge(db, "a", time.Time{}, false, t0)
	if ok, _ := ClaimDelivered(db, m3.ID, Consumer{By: "pop"}); !ok {
		t.Fatal("pop")
	}
	if got, _ := ClaimWakeRenudge(db, "a", time.Now()); len(got) != 0 {
		t.Fatalf("re-nudged shown/consumed rows: %v", ids(got))
	}
}

// Upgrading an alpha.38 DB (one shared bus_surfaced claim, no wake
// table): rows wake claimed there may never have reached the session, so
// the upgrade frees every pending claim — no row stays hidden. Consumed
// rows keep theirs, and later opens do not clear the hook record again.
func TestUpgradeFromSharedClaimFreesPendingRows(t *testing.T) {
	path := t.TempDir() + "/flow.db"
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	pending := putTaskMsg(t, db, "a")
	done := putTaskMsg(t, db, "a")
	if err := MarkSurfaced(db, "a", []string{pending.ID, done.ID}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := ClaimDelivered(db, done.ID, Consumer{By: "pop"}); !ok {
		t.Fatal("pop")
	}
	// Roll the DB back to the alpha.38 shape.
	if _, err := db.Exec(`DROP TABLE bus_wake_nudged`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db, err = OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got, _ := ClaimUnsurfaced(db, "a"); len(got) != 1 || got[0].ID != pending.ID {
		t.Fatalf("pending row still hidden after upgrade: %v", ids(got))
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_surfaced WHERE message_id=?`, done.ID).Scan(&n)
	if n != 1 {
		t.Errorf("consumed row's mark dropped: %d", n)
	}
	// A normal reopen keeps the hook record.
	db.Close()
	db, err = OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := ClaimUnsurfaced(db, "a"); len(got) != 0 {
		t.Fatalf("reopen cleared the hook record: %v", ids(got))
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
	putTaskMsg(t, db, "a")
	_, _ = ClaimWakeNudge(db, "a", time.Time{}, false, time.Now())
	if err := CleanupTaskBus(db, "a"); err != nil {
		t.Fatal(err)
	}
	if st, _ := GetSessionState(db, "a"); st.State != "" {
		t.Fatalf("session state survived cleanup: %+v", st)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_wake_nudged WHERE task_slug='a'`).Scan(&n)
	if n != 0 {
		t.Fatalf("wake record survived cleanup: %d", n)
	}
}

// nudged_at is compared as text, so it must be fixed-width: RFC3339Nano
// trims trailing zeros and '...:00Z' sorts after '...:00.5Z'. A push at a
// whole second is older than a cutoff half a second later.
func TestRenudgeCutoffSubSecondOrdering(t *testing.T) {
	db := wakeTestDB(t)
	m := putTaskMsg(t, db, "a")
	t0 := time.Now().Add(-time.Minute).Truncate(time.Second)
	if got, _ := ClaimWakeNudge(db, "a", time.Time{}, false, t0); len(got) != 1 {
		t.Fatal("nudge")
	}
	cut := t0.Add(500 * time.Millisecond)
	if !WakeDue(db, "a", time.Time{}, cut) {
		t.Error("WakeDue: a push at :00 is not before :00.5")
	}
	got, err := ClaimWakeRenudge(db, "a", cut)
	if err != nil || len(got) != 1 || got[0].ID != m.ID {
		t.Fatalf("renudge = %v %v", ids(got), err)
	}
}

// ClaimSurfacedIDs is the waiter's atomic hook-side claim: it returns only
// the ids THIS caller recorded, so rows a hook already announced (e.g. a
// PreToolUse racing the waiter) are not announced twice.
func TestClaimSurfacedIDsOnlyWon(t *testing.T) {
	db := wakeTestDB(t)
	m1 := putTaskMsg(t, db, "a")
	m2 := putTaskMsg(t, db, "a")
	_ = MarkSurfaced(db, "a", []string{m1.ID}) // the hook won m1
	got, err := ClaimSurfacedIDs(db, "a", []string{m1.ID, m2.ID})
	if err != nil || len(got) != 1 || got[0] != m2.ID {
		t.Fatalf("won = %v %v", got, err)
	}
	if again, _ := ClaimSurfacedIDs(db, "a", []string{m1.ID, m2.ID}); len(again) != 0 {
		t.Fatalf("second claim = %v", again)
	}
}
