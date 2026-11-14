package app

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness"
)

// reviveSetup: driver "d" (session sid-d, tagged driver, exited: no live
// push channel), its auto child "c", and an unrelated task "o". The
// probe says the session is not running and its transcript exists.
// Returns the DB and the launcher's env per call.
func reviveSetup(t *testing.T) (*sql.DB, *[][]string) {
	t.Helper()
	db := wakeSetup(t)
	t.Setenv(reviveOffEnv, "")
	mkBusTask(t, db, "d", "sid-d")
	mkBusTask(t, db, "c", "sid-c")
	mkBusTask(t, db, "o", "sid-o")
	if _, err := db.Exec(`UPDATE tasks SET parent_slug='d' WHERE slug='c'`); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.AddTaskTag(db, "d", driverTag); err != nil {
		t.Fatal(err)
	}
	stubWaker(t, &fakeWaker{live: false})
	stubReviveProbe(t, false, true)
	// The fake launcher's pids stand for live supervisors; without this a
	// wake-time reconcile would settle their runs as dead.
	oldAlive := processAlive
	processAlive = func(int) bool { return true }
	t.Cleanup(func() { processAlive = oldAlive })
	var envs [][]string
	old := autoLauncher
	autoLauncher = func(slug, workDir, logPath, injection string, env []string) (int, error) {
		envs = append(envs, env)
		return 9100 + len(envs), nil
	}
	t.Cleanup(func() { autoLauncher = old })
	return db, &envs
}

func stubReviveProbe(t *testing.T, live, onDisk bool) {
	t.Helper()
	old := reviveProbe
	reviveProbe = func(*flowdb.Task, harness.Waker) (bool, bool) { return live, onDisk }
	t.Cleanup(func() { reviveProbe = old })
}

// mail inserts a bus row from task from to task to and returns the
// sender-side outcome.
func mail(t *testing.T, db *sql.DB, id, from, to string, fyi bool) string {
	t.Helper()
	m := &flowdb.BusMessage{ID: id, CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: "user", FromTaskSlug: from, ToAssignee: "user", ToTaskSlug: to, Body: "child report", FYI: fyi}
	if err := flowdb.InsertBusMessage(db, m); err != nil {
		t.Fatal(err)
	}
	return wakeOutcome(id)
}

func hasEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

// The gap this closes: a child reports to its driver whose session has
// exited. Before revive the report sat unread ("no live push channel")
// until a human reopened the driver.
func TestReviveExitedDriverOnChildMail(t *testing.T) {
	db, envs := reviveSetup(t)

	out := mail(t, db, "r1", "c", "d", false)
	if len(*envs) != 1 {
		t.Fatalf("launches = %d, want 1 (out %q)", len(*envs), out)
	}
	if !strings.Contains(out, "woke d") || !strings.Contains(out, "revived") {
		t.Errorf("outcome = %q, want a revive report", out)
	}
	if !hasEnv((*envs)[0], reviveEnvVar+"=d") || !hasEnv((*envs)[0], autoRunEnvVar+"=d") {
		t.Errorf("revive env missing markers: %v", (*envs)[0])
	}
	task, _ := flowdb.GetTask(db, "d")
	if task.AutoRunStatus.String != "running" || task.AutoRunPID.Int64 != 9101 {
		t.Errorf("auto run = %q pid %d, want running 9101", task.AutoRunStatus.String, task.AutoRunPID.Int64)
	}
	// Single-flight: a second report while the revive runs starts nothing.
	out = mail(t, db, "r2", "c", "d", false)
	if len(*envs) != 1 || !strings.Contains(out, "stored: auto run") {
		t.Errorf("second report: launches=%d out=%q", len(*envs), out)
	}
	// The mail itself is untouched: the revived session pops it.
	if n, _ := flowdb.PendingCountForTask(db, "d"); n != 2 {
		t.Errorf("pending = %d, want 2", n)
	}
}

func TestReviveGates(t *testing.T) {
	cases := []struct {
		name string
		prep func(t *testing.T, db *sql.DB)
		from string
		fyi  bool
		want string
	}{
		{"sender is not its child", nil, "o", false, "no live push channel"},
		{"human sender", nil, "", false, "no live push channel"},
		{"fyi", nil, "c", true, "no live push channel"},
		{"not a driver", func(t *testing.T, db *sql.DB) {
			_, _ = db.Exec(`DELETE FROM task_tags WHERE task_slug='d'`)
		}, "c", false, "no live push channel"},
		{"revive off", func(t *testing.T, db *sql.DB) { t.Setenv(reviveOffEnv, "1") }, "c", false, "no live push channel"},
		{"wake off", func(t *testing.T, db *sql.DB) { t.Setenv(wakeOffEnv, "1") }, "c", false, "wake off"},
		{"maybe live", func(t *testing.T, db *sql.DB) { stubReviveProbe(t, true, true) }, "c", false, "may still be running"},
		{"hooks say mid-turn", func(t *testing.T, db *sql.DB) {
			_ = flowdb.SetSessionState(db, "d", flowdb.SessionBusy, time.Now())
		}, "c", false, "may still be running"},
		{"no transcript", func(t *testing.T, db *sql.DB) { stubReviveProbe(t, false, false) }, "c", false, "no transcript"},
		{"done", func(t *testing.T, db *sql.DB) {
			_, _ = db.Exec(`UPDATE tasks SET status='done' WHERE slug='d'`)
		}, "c", false, "task done"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db, envs := reviveSetup(t)
			if c.prep != nil {
				c.prep(t, db)
			}
			out := mail(t, db, "g1", c.from, "d", c.fyi)
			if len(*envs) != 0 {
				t.Fatalf("revived despite gate (out %q)", out)
			}
			if !strings.Contains(out, c.want) {
				t.Errorf("outcome = %q, want %q", out, c.want)
			}
		})
	}
}

func TestReviveRateLimited(t *testing.T) {
	db, envs := reviveSetup(t)
	old := reviveLimit
	reviveLimit = flowdb.ReviveLimit{Window: time.Hour, Max: 2}
	t.Cleanup(func() { reviveLimit = old })
	for i := 0; i < 3; i++ {
		out := mail(t, db, "rl"+string(rune('a'+i)), "c", "d", false)
		// Each revive ends before the next report.
		_, _ = db.Exec(`UPDATE tasks SET auto_run_status='revived', auto_run_pid=NULL WHERE slug='d'`)
		if i == 2 && !strings.Contains(out, "rate limit") {
			t.Errorf("third report: %q, want rate limit", out)
		}
	}
	if len(*envs) != 2 {
		t.Errorf("launches = %d, want 2", len(*envs))
	}
}

// A failed launch releases the claim, so the next report can try again.
func TestReviveLaunchFailureReleasesClaim(t *testing.T) {
	db, _ := reviveSetup(t)
	old := autoLauncher
	autoLauncher = func(string, string, string, string, []string) (int, error) { return 0, sql.ErrConnDone }
	t.Cleanup(func() { autoLauncher = old })
	out := mail(t, db, "f1", "c", "d", false)
	if !strings.Contains(out, "revive failed") {
		t.Errorf("outcome = %q", out)
	}
	task, _ := flowdb.GetTask(db, "d")
	if task.AutoRunStatus.Valid {
		t.Errorf("auto_run_status = %q after a failed launch, want NULL", task.AutoRunStatus.String)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_revives WHERE task_slug='d'`).Scan(&n)
	if n != 0 {
		t.Errorf("rate-limit rows = %d after a failed launch, want 0", n)
	}
}

// The revive supervisor resumes with the revive prompt and ends
// 'revived': a driver that did not close itself is not a dead run, so
// nobody is told it crashed.
func TestReviveSupervisorEndsRevived(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "drv")
	seedRunningAuto(t, "drv", 4343)
	t.Setenv(reviveEnvVar, "drv")
	var prompt string
	stubAutoRunner(t, func(h harness.Harness, sessionID, p string, opts harness.LaunchOpts) error {
		prompt = p
		return nil
	})
	if rc := cmdAutoExec([]string{"drv"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	for _, want := range []string{"DRIVER", "resumed HEADLESSLY", "flow inbox pop"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("revive prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "Work end to end") {
		t.Errorf("revive used the autonomous task prompt")
	}
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "drv")
	if task.AutoRunStatus.String != reviveStatus {
		t.Errorf("auto_run_status = %q, want %s", task.AutoRunStatus.String, reviveStatus)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bus_messages`).Scan(&n)
	if n != 0 {
		t.Errorf("bus rows = %d, want 0 (no dead notice for a revive)", n)
	}
}

func TestAutoChildEnvStripsReviveMarker(t *testing.T) {
	t.Setenv(reviveEnvVar, "drv")
	for _, kv := range autoChildEnv() {
		if strings.HasPrefix(kv, reviveEnvVar+"=") {
			t.Fatalf("child env leaks %s", kv)
		}
	}
}

// Mail that lands after the revived session's last pop (its PreToolUse
// marked it busy, and no Stop catch runs in an auto run) gets one more
// revive from the supervisor once the session exits.
func TestReviveSupervisorRevivesMailLandedMidRun(t *testing.T) {
	db, envs := reviveSetup(t)
	if out := mail(t, db, "m0", "c", "d", false); !strings.Contains(out, "revived") {
		t.Fatalf("first report not revived: %q", out)
	}
	oldPID := supervisorPID
	supervisorPID = func() int { return 9101 }
	t.Cleanup(func() { supervisorPID = oldPID })
	t.Setenv(reviveEnvVar, "d")
	stubAutoRunner(t, func(h harness.Harness, sessionID, p string, opts harness.LaunchOpts) error {
		_ = flowdb.TouchSessionActivity(db, "d", time.Now())
		_, _ = db.Exec(`DELETE FROM bus_messages WHERE id='m0'`) // the session popped it
		mail(t, db, "m1", "c", "d", false)                       // lands mid-run: held
		return nil
	})
	if rc := cmdAutoExec([]string{"d"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if len(*envs) != 2 {
		t.Fatalf("launches = %d, want 2 (one re-revive for m1)", len(*envs))
	}
	task, _ := flowdb.GetTask(db, "d")
	if task.AutoRunStatus.String != "running" {
		t.Errorf("auto_run_status = %q, want running (the re-revive)", task.AutoRunStatus.String)
	}
}
