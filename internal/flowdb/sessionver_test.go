package flowdb

import (
	"sync"
	"testing"
	"time"
)

func TestClaimSessionVersionNoRowWinsOnceAsUnknown(t *testing.T) {
	db := wakeTestDB(t)
	now := time.Now()
	old, known, won, err := ClaimSessionVersion(db, "s1", "v2", now)
	if err != nil || !won || known || old != "" {
		t.Fatalf("first claim on a session with no row: old=%q known=%v won=%v err=%v; want won as unknown", old, known, won, err)
	}
	if _, _, won, _ := ClaimSessionVersion(db, "s1", "v2", now); won {
		t.Fatal("second claim at the same version must not win")
	}
}

func TestRecordThenClaimSameVersionIsSilent(t *testing.T) {
	db := wakeTestDB(t)
	if err := RecordSessionVersion(db, "s1", "v1", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, won, _ := ClaimSessionVersion(db, "s1", "v1", time.Now()); won {
		t.Fatal("claim at the recorded version must not win")
	}
}

func TestClaimAfterVersionChangeReturnsOld(t *testing.T) {
	db := wakeTestDB(t)
	_ = RecordSessionVersion(db, "s1", "v1", time.Now())
	old, known, won, err := ClaimSessionVersion(db, "s1", "v2", time.Now())
	if err != nil || !won || !known || old != "v1" {
		t.Fatalf("old=%q known=%v won=%v err=%v; want won with old v1", old, known, won, err)
	}
	if _, _, won, _ := ClaimSessionVersion(db, "s1", "v2", time.Now()); won {
		t.Fatal("must not repeat")
	}
	// Every change notifies, including a step back.
	if old, _, won, _ := ClaimSessionVersion(db, "s1", "v1", time.Now()); !won || old != "v2" {
		t.Fatalf("downgrade: won=%v old=%q; want won with old v2", won, old)
	}
}

func TestRecordSessionVersionOverwritesWithoutClaim(t *testing.T) {
	db := wakeTestDB(t)
	_ = RecordSessionVersion(db, "s1", "v1", time.Now())
	_ = RecordSessionVersion(db, "s1", "v2", time.Now()) // a resume on the new binary
	if _, _, won, _ := ClaimSessionVersion(db, "s1", "v2", time.Now()); won {
		t.Fatal("SessionStart already recorded v2; no notice due")
	}
}

func TestClaimSessionVersionConcurrentOnlyOneWins(t *testing.T) {
	for _, seed := range []bool{true, false} { // existing row, and no row
		db := wakeTestDB(t)
		if seed {
			_ = RecordSessionVersion(db, "s1", "v1", time.Now())
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		wins := 0
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _, won, err := ClaimSessionVersion(db, "s1", "v2", time.Now())
				if err != nil {
					t.Error(err)
				}
				if won {
					mu.Lock()
					wins++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if wins != 1 {
			t.Errorf("seed=%v: %d claims won, want exactly 1", seed, wins)
		}
	}
}

func TestRecordSessionVersionPrunesStaleRows(t *testing.T) {
	db := wakeTestDB(t)
	long := time.Now().Add(-SessionVersionTTL - time.Hour)
	_ = RecordSessionVersion(db, "old", "v1", long)
	_ = RecordSessionVersion(db, "new", "v1", time.Now())
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM session_versions WHERE session_id='old'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("a row untouched past the TTL should be pruned")
	}
}
