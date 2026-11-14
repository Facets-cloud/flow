package app

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness/claude"
)

// Edge cases for driver revive (revive.go), from the PR #168 review.

func stubProcessAlive(t *testing.T, fn func(int) bool) {
	t.Helper()
	old := processAlive
	processAlive = fn
	t.Cleanup(func() { processAlive = old })
}

// A supervisor that crashed (kill -9, reboot) leaves auto_run_status
// 'running' with a dead pid. Read paths reconcile it, but nothing forces a
// read: without reconciling at wake time, every later child report hit the
// "auto run" gate and the driver was never revived again.
func TestReviveReconcilesStaleRunningRow(t *testing.T) {
	db, envs := reviveSetup(t)
	stubProcessAlive(t, func(pid int) bool { return pid != 4444 })
	if _, err := db.Exec(`UPDATE tasks SET auto_run_status='running', auto_run_pid=4444,
		auto_run_started=? WHERE slug='d'`, flowdb.NowISO()); err != nil {
		t.Fatal(err)
	}
	out := mail(t, db, "s1", "c", "d", false)
	if len(*envs) != 1 || !strings.Contains(out, "revived") {
		t.Fatalf("stale running row blocked the revive: launches=%d out=%q", len(*envs), out)
	}
}

// A live run (pid alive) still holds the mail: reconcile must not steal it.
func TestReviveLiveRunStillHolds(t *testing.T) {
	db, envs := reviveSetup(t)
	stubProcessAlive(t, func(int) bool { return true })
	if _, err := db.Exec(`UPDATE tasks SET auto_run_status='running', auto_run_pid=4444 WHERE slug='d'`); err != nil {
		t.Fatal(err)
	}
	out := mail(t, db, "s2", "c", "d", false)
	if len(*envs) != 0 || !strings.Contains(out, "stored: auto run") {
		t.Fatalf("launches=%d out=%q, want held by the live run", len(*envs), out)
	}
}

// A failed launch must restore the prior run's whole record, not just its
// status: a 'revived' row kept its started/finished times before the claim.
func TestReviveLaunchFailureRestoresPriorRun(t *testing.T) {
	db, _ := reviveSetup(t)
	const started, finished = "2026-01-02T03:04:05Z", "2026-01-02T03:14:05Z"
	if _, err := db.Exec(`UPDATE tasks SET auto_run_status='revived', auto_run_started=?, auto_run_finished=?
		WHERE slug='d'`, started, finished); err != nil {
		t.Fatal(err)
	}
	old := autoLauncher
	autoLauncher = func(string, string, string, string, []string) (int, error) { return 0, errors.New("boom") }
	t.Cleanup(func() { autoLauncher = old })
	if out := mail(t, db, "lf", "c", "d", false); !strings.Contains(out, "revive failed") {
		t.Fatalf("outcome = %q", out)
	}
	task, _ := flowdb.GetTask(db, "d")
	if task.AutoRunStatus.String != "revived" || task.AutoRunStarted.String != started ||
		task.AutoRunFinished.String != finished || task.AutoRunPID.Valid {
		t.Errorf("after release: status=%q started=%q finished=%q pid=%v, want the prior run restored",
			task.AutoRunStatus.String, task.AutoRunStarted.String, task.AutoRunFinished.String, task.AutoRunPID)
	}
}

// The claim and its rate-limit row are one transaction: when recording
// the row fails, the task must not be left 'running' (held forever).
func TestClaimReviveAtomic(t *testing.T) {
	db, _ := reviveSetup(t)
	if _, err := db.Exec(`CREATE TRIGGER bus_revives_fail BEFORE INSERT ON bus_revives
		BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := flowdb.ClaimRevive(db, "d", time.Now(), reviveLimit, 1); err == nil {
		t.Fatal("claim succeeded despite the failed insert")
	}
	task, _ := flowdb.GetTask(db, "d")
	if task.AutoRunStatus.Valid && task.AutoRunStatus.String == "running" {
		t.Errorf("auto_run_status left 'running' after a failed claim")
	}
}

// bus_revives only matters inside the window: a claim prunes older rows,
// so the table does not grow forever.
func TestClaimRevivePrunesOldRows(t *testing.T) {
	db, _ := reviveSetup(t)
	now := time.Now()
	for i := 0; i < 5; i++ {
		ts := now.Add(-time.Duration(2+i) * time.Hour).UTC().Format(time.RFC3339)
		if _, err := db.Exec(`INSERT INTO bus_revives (task_slug, revived_at) VALUES ('d', ?)`, ts); err != nil {
			t.Fatal(err)
		}
	}
	if c, err := flowdb.ClaimRevive(db, "d", now, reviveLimit, 1); err != nil || c != flowdb.ReviveClaimed {
		t.Fatalf("claim = %v, %v", c, err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_revives`).Scan(&n)
	if n != 1 {
		t.Errorf("bus_revives rows = %d, want 1 (rows outside the window pruned)", n)
	}
}

// The window is (now-Window, now]: a row exactly Window old no longer
// counts; one a second younger does.
func TestClaimReviveWindowBoundary(t *testing.T) {
	for _, c := range []struct {
		name string
		age  time.Duration
		want flowdb.ReviveClaim
	}{
		{"exactly window old", time.Hour, flowdb.ReviveClaimed},
		{"inside the window", time.Hour - time.Second, flowdb.ReviveRateLimited},
	} {
		t.Run(c.name, func(t *testing.T) {
			db, _ := reviveSetup(t)
			now := time.Now().Truncate(time.Second)
			ts := now.Add(-c.age).UTC().Format(time.RFC3339)
			if _, err := db.Exec(`INSERT INTO bus_revives (task_slug, revived_at) VALUES ('d', ?)`, ts); err != nil {
				t.Fatal(err)
			}
			got, err := flowdb.ClaimRevive(db, "d", now, flowdb.ReviveLimit{Window: time.Hour, Max: 1}, 1)
			if err != nil || got != c.want {
				t.Errorf("claim = %v, %v; want %v", got, err, c.want)
			}
		})
	}
}

// Concurrent senders (separate connections, as separate processes would
// have): exactly one claim wins.
func TestClaimReviveSingleFlightConcurrent(t *testing.T) {
	reviveSetup(t)
	path, err := flowDBPath()
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	results := make([]flowdb.ReviveClaim, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := openConcurrentDB(path)
			if err != nil {
				errs[i] = err
				return
			}
			defer c.Close()
			results[i], errs[i] = flowdb.ClaimRevive(c, "d", time.Now(), reviveLimit, 100+i)
		}(i)
	}
	wg.Wait()
	won := 0
	for i := range results {
		if errs[i] != nil {
			t.Errorf("claim %d: %v", i, errs[i])
			continue
		}
		if results[i] == flowdb.ReviveClaimed {
			won++
		} else if results[i] != flowdb.ReviveInFlight {
			t.Errorf("claim %d = %v, want in-flight", i, results[i])
		}
	}
	if won != 1 {
		t.Errorf("winners = %d, want exactly 1", won)
	}
}

// Every failed revive used to FYI the human again: a child writing 4 times
// an hour to a driver whose resume keeps failing meant 4 identical FYIs.
// While one is still unread, the next failure adds none.
func TestFinishReviveDeadFYIDeduped(t *testing.T) {
	db, _ := reviveSetup(t)
	task, _ := flowdb.GetTask(db, "d")
	finishRevive(db, task, "dead", flowdb.NowISO())
	finishRevive(db, task, "dead", flowdb.NowISO())
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_messages WHERE to_task_slug IS NULL AND from_task_slug='d'`).Scan(&n)
	if n != 1 {
		t.Errorf("human FYIs = %d, want 1 while the first is unread", n)
	}
	// Once the human has seen it, a new failure is news again.
	_, _ = db.Exec(`UPDATE bus_messages SET seen_at=? WHERE from_task_slug='d'`, flowdb.NowISO())
	finishRevive(db, task, "dead", flowdb.NowISO())
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_messages WHERE to_task_slug IS NULL AND from_task_slug='d'`).Scan(&n)
	if n != 2 {
		t.Errorf("human FYIs = %d after the first was seen, want 2", n)
	}
}

// Mail a PreToolUse hook announced mid-run but the session never popped
// is still unread when the revive exits: it must get the re-check too,
// or it sits until a human reopens the driver.
func TestReviveRecheckCoversSurfacedButUnpoppedMail(t *testing.T) {
	db, envs := reviveSetup(t)
	start := time.Now().Add(-time.Minute).Format(time.RFC3339)
	mail(t, db, "late", "c", "d", false) // starts revive #1; reset it below
	*envs = nil
	_ = flowdb.MarkSurfaced(db, "d", []string{"late"})
	_, _ = db.Exec(`UPDATE tasks SET auto_run_status='revived', auto_run_pid=NULL WHERE slug='d'`)
	_, _ = db.Exec(`DELETE FROM bus_revives`)
	reviveIfMailLanded(db, "d", start)
	if len(*envs) != 1 {
		t.Errorf("launches = %d, want 1 for the surfaced-but-unread report", len(*envs))
	}
}

// reviveIfMailLanded: a bad start time does nothing; mail from before the
// revive started is not its business; a report exactly at the start counts.
func TestReviveIfMailLandedBounds(t *testing.T) {
	cases := []struct {
		name    string
		start   func(created time.Time) string
		created time.Time
		want    int
	}{
		{"unparseable start", func(time.Time) string { return "yesterday" }, time.Now(), 0},
		{"mail before start", func(c time.Time) string { return c.Add(time.Minute).Format(time.RFC3339) }, time.Now(), 0},
		{"mail exactly at start", func(c time.Time) string { return c.Format(time.RFC3339) }, time.Now(), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, envs := reviveSetup(t)
			m := &flowdb.BusMessage{ID: "b1", CreatedAt: c.created.Format(time.RFC3339), Kind: "message",
				FromAssignee: "user", FromTaskSlug: "c", ToAssignee: "user", ToTaskSlug: "d", Body: "x"}
			// Insert without the wake hook: this is mail already in the inbox.
			oldAfter := flowdb.AfterInsert
			flowdb.AfterInsert = nil
			err := flowdb.InsertBusMessage(db, m)
			flowdb.AfterInsert = oldAfter
			if err != nil {
				t.Fatal(err)
			}
			reviveIfMailLanded(db, "d", c.start(c.created))
			if len(*envs) != c.want {
				t.Errorf("launches = %d, want %d", len(*envs), c.want)
			}
		})
	}
}

func TestIsChildReportEdges(t *testing.T) {
	db, _ := reviveSetup(t)
	mk := func(from, to string) *flowdb.BusMessage {
		return &flowdb.BusMessage{Kind: "message", FromTaskSlug: from, ToTaskSlug: to}
	}
	if !isChildReport(db, mk("c", "d")) {
		t.Error("child → driver should count")
	}
	// The driver writing to its child must never revive the child: no
	// ping-pong, even when the child is tagged driver too.
	_ = flowdb.AddTaskTag(db, "c", driverTag)
	if isChildReport(db, mk("d", "c")) {
		t.Error("driver → child counted as a child report")
	}
	if isChildReport(db, mk("c", "o")) {
		t.Error("child → an unrelated task counted")
	}
	if isChildReport(db, mk("gone", "d")) {
		t.Error("a deleted sender counted")
	}
	b := mk("c", "d")
	b.Kind = "broadcast"
	if isChildReport(db, b) {
		t.Error("a broadcast counted")
	}
}

func TestReviveSkipsArchivedDriver(t *testing.T) {
	db, envs := reviveSetup(t)
	_, _ = db.Exec(`UPDATE tasks SET archived_at=? WHERE slug='d'`, flowdb.NowISO())
	out := mail(t, db, "a1", "c", "d", false)
	if len(*envs) != 0 || !strings.Contains(out, "archived") {
		t.Errorf("launches=%d out=%q", len(*envs), out)
	}
}

// The real probe: a nil Waker must not panic; a ps failure or a running
// process (any id case) means live.
func TestReviveProbeReal(t *testing.T) {
	db, _ := reviveSetup(t)
	reviveProbe = defaultReviveProbe
	task, _ := flowdb.GetTask(db, "d")
	task.SessionID = sql.NullString{String: "AbCdEf00-0000-4000-8000-000000000001", Valid: true}
	oldPS := claude.PSRunner
	t.Cleanup(func() { claude.PSRunner = oldPS })

	claude.PSRunner = func() ([]byte, error) { return nil, errors.New("ps broke") }
	if live, _ := reviveProbe(task, nil); !live {
		t.Error("ps failure must count as live")
	}
	claude.PSRunner = func() ([]byte, error) {
		return []byte("123 claude --resume abcdef00-0000-4000-8000-000000000001 -p hi\n"), nil
	}
	if live, _ := reviveProbe(task, nil); !live {
		t.Error("a running process (case-insensitive id) must count as live")
	}
	claude.PSRunner = func() ([]byte, error) { return []byte(""), nil }
	if live, onDisk := reviveProbe(task, nil); live || onDisk {
		t.Errorf("no process, no transcript: live=%v onDisk=%v, want false,false", live, onDisk)
	}
}
