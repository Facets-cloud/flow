package app

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness"
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
// revive started, or from its start second, is not its business.
func TestReviveIfMailLandedBounds(t *testing.T) {
	cases := []struct {
		name    string
		start   func(created time.Time) string
		created time.Time
		want    int
	}{
		{"unparseable start", func(time.Time) string { return "yesterday" }, time.Now(), 0},
		{"mail before start", func(c time.Time) string { return c.Add(time.Minute).Format(time.RFC3339) }, time.Now(), 0},
		// Second precision: a report stamped in the revive's start second is the
		// one that triggered it (e2e: a revive that did not pop it re-revived).
		{"mail in the start second", func(c time.Time) string { return c.Format(time.RFC3339) }, time.Now(), 0},
		{"mail after start", func(c time.Time) string { return c.Add(-2 * time.Second).Format(time.RFC3339) }, time.Now(), 1},
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

// plainWaker has no TurnStateChecker: a harness that cannot cross-check a
// session's turn (Codex), where hook state is the only busy signal.
type plainWaker struct{}

func (plainWaker) WakeLive(harness.WakeRequest) bool        { return false }
func (plainWaker) Wake(harness.WakeRequest) (string, error) { return "", nil }

// e2e bug 1: a driver closed mid-turn (or one that last ran as an auto
// run, whose Stop hook never records idle) keeps hook state 'busy'. With a
// harness that can cross-check the turn (Claude), the probe already said
// not running, so stale hook state must not refuse the revive.
func TestReviveIgnoresHookBusyWhenHarnessCanCheck(t *testing.T) {
	db, envs := reviveSetup(t) // fakeWaker is a TurnStateChecker
	_ = flowdb.SetSessionState(db, "d", flowdb.SessionBusy, time.Now())
	if out := mail(t, db, "hb", "c", "d", false); len(*envs) != 1 {
		t.Errorf("launches = %d (out %q), want a revive", len(*envs), out)
	}
}

// Without a turn cross-check, recent hook activity still means live.
func TestReviveHookBusyGuardWithoutTurnCheck(t *testing.T) {
	db, envs := reviveSetup(t)
	old := wakeAdapterFor
	wakeAdapterFor = func(*flowdb.Task) harness.Waker { return plainWaker{} }
	t.Cleanup(func() { wakeAdapterFor = old })
	_ = flowdb.SetSessionState(db, "d", flowdb.SessionBusy, time.Now())
	if out := mail(t, db, "hc", "c", "d", false); len(*envs) != 0 || !strings.Contains(out, "may still be running") {
		t.Errorf("launches = %d out %q, want refused", len(*envs), out)
	}
}

// e2e bug 1 (second half): any auto run's process is gone once its
// supervisor finalizes, so its session is idle whatever the hooks said.
func TestAutoExecLeavesSessionIdle(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ar")
	seedRunningAuto(t, "ar", 4545)
	db := openFlowDB(t)
	_ = flowdb.SetSessionState(db, "ar", flowdb.SessionBusy, time.Now())
	stubAutoRunner(t, func(harness.Harness, string, string, harness.LaunchOpts) error { return nil })
	cmdAutoExec([]string{"ar"})
	if st, _ := flowdb.GetSessionState(db, "ar"); st.State == flowdb.SessionBusy {
		t.Errorf("session state = busy after the auto run exited")
	}
}

// e2e bug 4: the claim stamps the SENDER's pid. A supervisor that finished
// before the sender recorded its pid could not finalize (not its pid), and
// the sender's late record then left the row 'running' with a dead pid.
func TestReviveFastSupervisorFinalizes(t *testing.T) {
	db, envs := reviveSetup(t)
	oldPID := supervisorPID
	t.Cleanup(func() { supervisorPID = oldPID })
	stubAutoRunner(t, func(harness.Harness, string, string, harness.LaunchOpts) error { return nil })
	autoLauncher = func(slug, workDir, logPath, injection string, env []string) (int, error) {
		*envs = append(*envs, env)
		// The supervisor runs to completion before launch returns.
		supervisorPID = func() int { return 9301 }
		t.Setenv(reviveEnvVar, slug)
		cmdAutoExec([]string{slug})
		return 9301, nil
	}
	mail(t, db, "fs", "c", "d", false)
	task, _ := flowdb.GetTask(db, "d")
	if task.AutoRunStatus.String != reviveStatus || task.AutoRunPID.Valid {
		t.Errorf("auto run = %q pid %v, want %s with no pid", task.AutoRunStatus.String, task.AutoRunPID, reviveStatus)
	}
}

func TestAutoRunColorRevived(t *testing.T) {
	if autoRunColor(reviveStatus) == "" {
		t.Error("revived has no color in flow list")
	}
}

// ReleaseRevive drops exactly its own rate-limit row, even when another
// row shares the second.
func TestReleaseReviveDropsOneRow(t *testing.T) {
	db, _ := reviveSetup(t)
	now := time.Now()
	ts := now.UTC().Format(time.RFC3339)
	_, _ = db.Exec(`INSERT INTO bus_revives (task_slug, revived_at) VALUES ('d', ?)`, ts)
	if c, err := flowdb.ClaimRevive(db, "d", now, reviveLimit, 7); err != nil || c != flowdb.ReviveClaimed {
		t.Fatalf("claim = %v, %v", c, err)
	}
	if err := flowdb.ReleaseRevive(db, "d", 7, now, flowdb.AutoRunRecord{}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_revives WHERE task_slug='d'`).Scan(&n)
	if n != 1 {
		t.Errorf("rows = %d, want 1 (only the released claim's row dropped)", n)
	}
}

// A refused revive leaves the child's report unread with only a headless
// sender told why: the human gets ONE FYI naming the driver and reason.
// (A driver that may still be running is not FYI'd: its hooks deliver —
// TestReviveLiveDriverNoHumanFYI.)
func TestReviveRefusalFYIsHuman(t *testing.T) {
	humanFYIs := func(db *sql.DB) []string {
		ms, _ := flowdb.UnreadForHuman(db, busUser)
		var out []string
		for _, m := range ms {
			if m.FromTaskSlug == "d" {
				out = append(out, m.Body)
			}
		}
		return out
	}
	cases := []struct {
		name, reason string
		prep         func(t *testing.T, db *sql.DB)
	}{
		{"rate limit", "rate limit", func(t *testing.T, db *sql.DB) {
			old := reviveLimit
			reviveLimit = flowdb.ReviveLimit{Window: time.Hour, Max: 0}
			t.Cleanup(func() { reviveLimit = old })
		}},
		{"no transcript", "no transcript", func(t *testing.T, db *sql.DB) { stubReviveProbe(t, false, false) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, envs := reviveSetup(t)
			c.prep(t, db)
			mail(t, db, "x1", "c", "d", false)
			mail(t, db, "x2", "c", "d", false)
			got := humanFYIs(db)
			if len(*envs) != 0 || len(got) != 1 {
				t.Fatalf("launches=%d human FYIs=%d %q, want 0 and 1", len(*envs), len(got), got)
			}
			if !strings.Contains(got[0], "d") || !strings.Contains(got[0], c.reason) {
				t.Errorf("FYI %q should name the driver and %q", got[0], c.reason)
			}
		})
	}
}

// Non-eligible mail and an in-flight revive are not refusals worth a ping.
func TestReviveNoRefusalFYIForNormalHolds(t *testing.T) {
	db, envs := reviveSetup(t)
	mail(t, db, "n1", "o", "d", false) // not its child
	mail(t, db, "n2", "c", "d", false) // revives
	mail(t, db, "n3", "c", "d", false) // held by the running revive
	ms, _ := flowdb.UnreadForHuman(db, busUser)
	if len(*envs) != 1 || len(ms) != 0 {
		t.Errorf("launches=%d human mail=%d, want 1 and 0", len(*envs), len(ms))
	}
}
