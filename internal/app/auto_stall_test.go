package app

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"flow/internal/flowdb"
)

// The stall fires once the run has been silent for the threshold, and not before.
func TestStallWatchFiresAfterThreshold(t *testing.T) {
	start := time.Unix(1000, 0)
	w := stallWatch{threshold: 10 * time.Minute, seen: start}
	if w.observe(time.Time{}, start.Add(9*time.Minute)) {
		t.Fatal("fired before threshold")
	}
	if !w.observe(time.Time{}, start.Add(10*time.Minute)) {
		t.Fatal("did not fire at threshold")
	}
}

// One message per stall episode: repeated silent samples stay quiet.
func TestStallWatchOncePerEpisode(t *testing.T) {
	start := time.Unix(1000, 0)
	w := stallWatch{threshold: time.Minute, seen: start}
	fired := 0
	for i := 1; i <= 10; i++ {
		if w.observe(time.Time{}, start.Add(time.Duration(i)*time.Minute)) {
			fired++
		}
	}
	if fired != 1 {
		t.Fatalf("fired %d times in one episode, want 1", fired)
	}
}

// Activity resets the episode: after it moves, a new silence can fire again.
func TestStallWatchResetsOnActivity(t *testing.T) {
	start := time.Unix(1000, 0)
	w := stallWatch{threshold: time.Minute, seen: start}
	if !w.observe(time.Time{}, start.Add(time.Minute)) {
		t.Fatal("first stall did not fire")
	}
	resumed := start.Add(90 * time.Second)
	if w.observe(resumed, resumed) {
		t.Fatal("fired on the sample that carried activity")
	}
	if !w.observe(resumed, resumed.Add(time.Minute)) {
		t.Fatal("second stall after activity did not fire")
	}
}

// An activity time at or before the last seen one is no news and does not reset.
func TestStallWatchOldActivityIsNoNews(t *testing.T) {
	start := time.Unix(1000, 0)
	w := stallWatch{threshold: time.Minute, seen: start}
	if !w.observe(time.Time{}, start.Add(time.Minute)) {
		t.Fatal("stall did not fire")
	}
	if w.observe(start, start.Add(2*time.Minute)) {
		t.Fatal("stale activity re-armed the episode")
	}
}

// After stop returns, the sampler is gone: no stall fires for a finished run.
func TestStallWatchdogStopSilencesSampler(t *testing.T) {
	var fired atomic.Int32
	stop := startStallWatchdog(time.Nanosecond, time.Millisecond,
		func() time.Time { return time.Time{} },
		func(time.Duration) { fired.Add(1) })
	time.Sleep(20 * time.Millisecond)
	stop()
	at := fired.Load()
	time.Sleep(30 * time.Millisecond)
	if got := fired.Load(); got != at {
		t.Fatalf("notice fired after stop: %d then %d", at, got)
	}
}

// Activity is read from the log mtime and the stall reports the silence; a
// fresh log means the run is not stalled, an old one is, and a finished run
// never is.
func TestAutoRunStalledForUsesLogActivity(t *testing.T) {
	t.Setenv(autoStallEnvVar, "5")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")
	if err := os.WriteFile(logPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * time.Minute)
	if err := os.Chtimes(logPath, old, old); err != nil {
		t.Fatal(err)
	}
	task := &flowdb.Task{
		Slug:           "kid",
		AutoRunStatus:  nullStr("running"),
		AutoRunLog:     nullStr(logPath),
		AutoRunStarted: nullStr(time.Now().Add(-time.Hour).Format(time.RFC3339)),
	}
	if _, stalled := autoRunStalledFor(task, time.Now()); !stalled {
		t.Fatal("silent running run not reported stalled")
	}
	if err := os.Chtimes(logPath, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, stalled := autoRunStalledFor(task, time.Now()); stalled {
		t.Fatal("run with fresh activity reported stalled")
	}
	task.AutoRunStatus = nullStr("completed")
	if _, stalled := autoRunStalledFor(task, time.Now().Add(time.Hour)); stalled {
		t.Fatal("completed run reported stalled")
	}
}

// A parented stalled run messages the parent once with slug, minutes and the
// last log line; a parentless run messages nobody.
func TestNotifyParentOfStalledChild(t *testing.T) {
	root := setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "papa", "sid-papa")
	seedDeadPingTask(t, root, "kid")
	if _, err := db.Exec(`UPDATE tasks SET parent_slug='papa' WHERE slug='kid'`); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "tasks", "kid", "auto-runs", "2026-11-09-120000.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("earlier\nopened the browser tab\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	child, err := flowdb.GetTask(db, "kid")
	if err != nil {
		t.Fatal(err)
	}
	notifyParentOfStalledChild(db, child, 18*time.Minute)
	rows := deadPingRows(t)
	if len(rows) != 1 || rows[0].toS != "papa" {
		t.Fatalf("want one parent notice, got %+v", rows)
	}
	for _, want := range []string{"kid", "stalled", "18 min", "opened the browser tab"} {
		if !strings.Contains(rows[0].body, want) {
			t.Errorf("body missing %q: %s", want, rows[0].body)
		}
	}

	seedDeadPingTask(t, root, "orphan")
	orphan, _ := flowdb.GetTask(db, "orphan")
	notifyParentOfStalledChild(db, orphan, 18*time.Minute)
	if n := len(deadPingRows(t)); n != 1 {
		t.Fatalf("parentless stall messaged someone: %d rows", n)
	}
}

func nullStr(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

// An override too large for a Duration is clamped, never wrapped negative:
// a negative threshold would report every run as stalled on its first sample.
func TestAutoStallThresholdClampsHugeOverride(t *testing.T) {
	t.Setenv(autoStallEnvVar, "99999999999999")
	if got := autoStallThreshold(); got <= 0 {
		t.Fatalf("threshold = %v, want positive", got)
	}
	t.Setenv(autoStallEnvVar, "0")
	if got := autoStallThreshold(); got != autoStallDefault {
		t.Fatalf("zero override = %v, want default %v", got, autoStallDefault)
	}
}

// The watchdog counts silence from the last recorded activity, so an
// already-silent run is reported on the first check, matching `flow list`.
func TestStallWatchdogSeedsFromRecordedActivity(t *testing.T) {
	var fired atomic.Int32
	old := time.Now().Add(-time.Hour)
	stop := startStallWatchdog(time.Minute, time.Millisecond,
		func() time.Time { return old },
		func(time.Duration) { fired.Add(1) })
	time.Sleep(30 * time.Millisecond)
	stop()
	if fired.Load() != 1 {
		t.Fatalf("fired = %d, want 1 on the first check", fired.Load())
	}
}
