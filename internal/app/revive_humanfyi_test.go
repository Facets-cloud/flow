package app

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flow/internal/flowdb"
)

// Human-messaging edges of driver revive, from the PR #168 regression e2e
// (findings L1–L5).

// humanMailFrom returns the human's unread mail sent from slug.
func humanMailFrom(db *sql.DB, slug string) []*flowdb.BusMessage {
	ms, _ := flowdb.UnreadForHuman(db, busUser)
	var out []*flowdb.BusMessage
	for _, m := range ms {
		if m.FromTaskSlug == slug {
			out = append(out, m)
		}
	}
	return out
}

// insertQuiet stores mail without the wake hook: mail already in the inbox.
func insertQuiet(t *testing.T, db *sql.DB, m *flowdb.BusMessage) {
	t.Helper()
	old := flowdb.AfterInsert
	flowdb.AfterInsert = nil
	defer func() { flowdb.AfterInsert = old }()
	if err := flowdb.InsertBusMessage(db, m); err != nil {
		t.Fatal(err)
	}
}

// L1: a driver that may still be running has hooks that deliver the mail,
// and `flow do` would hit the live-session guard: telling the human to
// reopen it is wrong. The sender still learns why.
func TestReviveLiveDriverNoHumanFYI(t *testing.T) {
	db, envs := reviveSetup(t)
	stubReviveProbe(t, true, true)
	out := mail(t, db, "l1", "c", "d", false)
	if len(*envs) != 0 || !strings.Contains(out, "may still be running") {
		t.Fatalf("launches=%d out=%q, want refused as live", len(*envs), out)
	}
	if got := humanMailFrom(db, "d"); len(got) != 0 {
		t.Errorf("human got %d messages for a live driver: %q", len(got), got[0].Body)
	}
}

// L2: the refusal FYI is deduped per (driver, reason). An unread FYI for
// one reason must not hide a different, newer reason.
func TestReviveRefusalFYIDedupPerReason(t *testing.T) {
	db, _ := reviveSetup(t)
	stubReviveProbe(t, false, false) // no transcript
	mail(t, db, "a1", "c", "d", false)
	mail(t, db, "a2", "c", "d", false)
	if got := humanMailFrom(db, "d"); len(got) != 1 {
		t.Fatalf("human FYIs = %d after two same-reason refusals, want 1", len(got))
	}
	stubReviveProbe(t, false, true)
	old := reviveLimit
	reviveLimit = flowdb.ReviveLimit{Window: time.Hour, Max: 0}
	t.Cleanup(func() { reviveLimit = old })
	mail(t, db, "a3", "c", "d", false)
	mail(t, db, "a4", "c", "d", false)
	got := humanMailFrom(db, "d")
	if len(got) != 2 {
		t.Fatalf("human FYIs = %d, want 2 (one per reason)", len(got))
	}
	if !strings.Contains(got[1].Body, "rate limit") {
		t.Errorf("newer FYI %q should name the rate limit", got[1].Body)
	}
}

// L3: a revive that ends 'revived' with child reports still unread (the
// model ignored the prompt or exited early; reports stamped in the start
// second are not re-revived) tells the human ONCE, and does not revive.
func TestReviveRevivedWithUnreadReportsFYIsHuman(t *testing.T) {
	db, envs := reviveSetup(t)
	start := time.Now().Truncate(time.Second)
	for _, id := range []string{"u1", "u2"} {
		insertQuiet(t, db, &flowdb.BusMessage{ID: id, CreatedAt: start.Format(time.RFC3339), Kind: "message",
			FromAssignee: "user", FromTaskSlug: "c", ToAssignee: "user", ToTaskSlug: "d", Body: "report"})
	}
	task, _ := flowdb.GetTask(db, "d")
	finishRevive(db, task, reviveStatus, start.Format(time.RFC3339))
	finishRevive(db, task, reviveStatus, start.Format(time.RFC3339))
	if len(*envs) != 0 {
		t.Errorf("launches = %d, want 0 (no re-revive for start-second mail)", len(*envs))
	}
	got := humanMailFrom(db, "d")
	if len(got) != 1 {
		t.Fatalf("human FYIs = %d, want 1 deduped", len(got))
	}
	if !got[0].FYI || !strings.Contains(got[0].Body, "2") || !strings.Contains(got[0].Body, "unread") {
		t.Errorf("FYI %+v should be an FYI counting 2 unread reports", got[0])
	}
}

// L3 contrast: a revive that drained its inbox sends nothing.
func TestReviveRevivedDrainedNoFYI(t *testing.T) {
	db, envs := reviveSetup(t)
	task, _ := flowdb.GetTask(db, "d")
	finishRevive(db, task, reviveStatus, time.Now().Format(time.RFC3339))
	if len(*envs) != 0 || len(humanMailFrom(db, "d")) != 0 {
		t.Errorf("launches=%d human mail=%d, want 0 and 0", len(*envs), len(humanMailFrom(db, "d")))
	}
}

// L4: a killed revive supervisor, reconciled when the next child report
// arrives, must not send the human the generic non-FYI "died … reopen"
// ask: the same send re-revives the driver.
func TestReviveDeadRevivRunReconcileNoReopenAsk(t *testing.T) {
	db, envs := reviveSetup(t)
	stubProcessAlive(t, func(pid int) bool { return pid != 4444 })
	started := time.Now().Add(-time.Minute).Truncate(time.Second)
	if _, err := db.Exec(`UPDATE tasks SET auto_run_status='running', auto_run_pid=4444, auto_run_started=?
		WHERE slug='d'`, started.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO bus_revives (task_slug, revived_at) VALUES ('d', ?)`,
		started.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	out := mail(t, db, "k1", "c", "d", false)
	if len(*envs) != 1 || !strings.Contains(out, "revived") {
		t.Fatalf("launches=%d out=%q, want a fresh revive", len(*envs), out)
	}
	for _, m := range humanMailFrom(db, "d") {
		if !m.FYI || strings.Contains(m.Body, "died without finishing") {
			t.Errorf("human got a reopen ask for a revive run: fyi=%v %q", m.FYI, m.Body)
		}
	}
}

// L4 contrast: the driver's own (non-revive) auto run dying still gets
// the generic dead-orphan notice.
func TestDeadNonReviveRunStillAsksHuman(t *testing.T) {
	db, _ := reviveSetup(t)
	stubProcessAlive(t, func(pid int) bool { return pid != 4444 })
	if _, err := db.Exec(`UPDATE tasks SET auto_run_status='running', auto_run_pid=4444, auto_run_started=?
		WHERE slug='d'`, time.Now().Add(-time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	mail(t, db, "k2", "c", "d", false)
	var ask bool
	for _, m := range humanMailFrom(db, "d") {
		if !m.FYI && strings.Contains(m.Body, "died without finishing") {
			ask = true
		}
	}
	if !ask {
		t.Error("a dead non-revive auto run lost its dead-orphan notice")
	}
}

// L5: a driver in backlog does not revive, but a child's report waiting
// on it is worth ONE human FYI.
func TestBacklogDriverChildReportFYIsHuman(t *testing.T) {
	db, envs := reviveSetup(t)
	if _, err := db.Exec(`UPDATE tasks SET status='backlog' WHERE slug='d'`); err != nil {
		t.Fatal(err)
	}
	out := mail(t, db, "b1", "c", "d", false)
	mail(t, db, "b2", "c", "d", false)
	mail(t, db, "b3", "o", "d", false) // not its child: no FYI of its own
	if len(*envs) != 0 || !strings.Contains(out, "task backlog") {
		t.Fatalf("launches=%d out=%q, want stored: task backlog", len(*envs), out)
	}
	got := humanMailFrom(db, "d")
	if len(got) != 1 || !got[0].FYI || !strings.Contains(got[0].Body, "backlog") {
		t.Fatalf("human mail = %d %v, want 1 backlog FYI", len(got), got)
	}
}

// L5: a non-driver backlog task stays silent, as before.
func TestBacklogNonDriverNoFYI(t *testing.T) {
	db, _ := reviveSetup(t)
	_, _ = db.Exec(`UPDATE tasks SET status='backlog' WHERE slug='d'`)
	_, _ = db.Exec(`DELETE FROM task_tags WHERE task_slug='d'`)
	mail(t, db, "b4", "c", "d", false)
	if got := humanMailFrom(db, "d"); len(got) != 0 {
		t.Errorf("human mail = %d, want 0 for a non-driver", len(got))
	}
}

// N1: a revive whose launch fails (unwritable log dir, fork failure)
// releases its claim, so the child's report sits unread with nothing
// surfacing it. The human gets ONE FYI while it is unread, even though
// each attempt's error text differs (the log name is timestamped).
func TestReviveLaunchFailureFYIsHumanOnce(t *testing.T) {
	db, _ := reviveSetup(t)
	old := autoLauncher
	n := 0
	autoLauncher = func(string, string, string, string, []string) (int, error) {
		n++
		return 0, fmt.Errorf("open log: open /x/run-%d.log: permission denied", n)
	}
	t.Cleanup(func() { autoLauncher = old })
	for _, id := range []string{"n1", "n2"} {
		if out := mail(t, db, id, "c", "d", false); !strings.Contains(out, "revive failed") {
			t.Fatalf("outcome = %q, want revive failed", out)
		}
	}
	got := humanMailFrom(db, "d")
	if len(got) != 1 {
		t.Fatalf("human FYIs = %d after two failed launches, want 1 deduped", len(got))
	}
	b := got[0].Body
	if !got[0].FYI || !strings.Contains(b, "could not launch") || !strings.Contains(b, "permission denied") ||
		!strings.Contains(b, "flow do d") {
		t.Errorf("FYI %+v should name the launch failure, its error and how to reopen", got[0])
	}
}

// N1: the launcher's log-open error names the log path once; the
// *os.PathError it wraps already carries it.
func TestAutoLauncherLogErrorNamesPathOnce(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "missing", "run.log")
	_, err := autoLauncher("s", t.TempDir(), logPath, "", nil)
	if err == nil {
		t.Fatal("launch succeeded with a missing log dir")
	}
	if c := strings.Count(err.Error(), logPath); c != 1 {
		t.Errorf("error %q names the log path %d times, want 1", err, c)
	}
}

// O1: a revive that drained the driver's child reports makes earlier
// revive FYIs stale ("did not revive", "could not launch"): flow marks
// them read. A backlog notice is not about a revive and stays.
func TestReviveDrainedRetractsStaleFYIs(t *testing.T) {
	db, _ := reviveSetup(t)
	stubReviveProbe(t, false, false) // no transcript → refusal FYI
	mail(t, db, "o1", "c", "d", false)
	fyiHumanOnce(db, "d", reviveBacklogPrefix+"d is in backlog", reviveBacklogPrefix+"d is in backlog; keep me")
	if got := humanMailFrom(db, "d"); len(got) != 2 {
		t.Fatalf("human FYIs = %d before the revive, want 2", len(got))
	}
	if _, err := db.Exec(`UPDATE bus_messages SET status='delivered' WHERE to_task_slug='d'`); err != nil {
		t.Fatal(err)
	}
	task, _ := flowdb.GetTask(db, "d")
	finishRevive(db, task, reviveStatus, time.Now().Format(time.RFC3339))
	got := humanMailFrom(db, "d")
	if len(got) != 1 || !strings.HasPrefix(got[0].Body, reviveBacklogPrefix) {
		t.Fatalf("human unread after a drained revive = %d, want only the backlog notice", len(got))
	}
}

// O1 contrast: a revive that left reports unread retracts nothing.
func TestReviveUndrainedKeepsRefusalFYI(t *testing.T) {
	db, _ := reviveSetup(t)
	stubReviveProbe(t, false, false)
	mail(t, db, "o2", "c", "d", false)
	task, _ := flowdb.GetTask(db, "d")
	finishRevive(db, task, reviveStatus, time.Now().Add(time.Hour).Format(time.RFC3339))
	for _, m := range humanMailFrom(db, "d") {
		if strings.HasPrefix(m.Body, reviveRefusedPrefix) {
			return
		}
	}
	t.Error("refusal FYI was retracted although the report is still unread")
}
