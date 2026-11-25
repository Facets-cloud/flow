package app

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
)

// PR #168 re-review (Pramod, 2026-11-23). One test (or pair) per item.

// Item 1a: the claim names the run's owner (auto_run_log) in the same write,
// so a supervisor that exits before the launcher records its pid still
// finalizes the row and sends the dead notice. Before, the claim stamped only
// flow do's pid, the supervisor's log-keyed finalize matched nothing, and the
// row was left 'running' with no notice.
func TestAutoRunEarlyExitFinalizesItsClaim(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "parent")
	seedTask(t, "kid")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET parent_slug='parent' WHERE slug='kid'`); err != nil {
		t.Fatal(err)
	}
	stubITerm(t)
	stubAutoRunner(t, func(harness.Harness, string, string, harness.LaunchOpts) error {
		return errors.New("harness crashed at once")
	})
	old := autoLauncher
	autoLauncher = func(slug, workDir, logPath, injection string, env []string) (int, error) {
		// The supervisor runs (and dies) before the launcher records it.
		captureStdout(t, func() { cmdAutoExec([]string{slug, "--log", logPath}) })
		return 9401, nil
	}
	t.Cleanup(func() { autoLauncher = old })

	if rc := cmdDo([]string{"kid", "--auto"}); rc != 0 {
		t.Fatalf("cmdDo --auto rc=%d", rc)
	}
	task, _ := flowdb.GetTask(db, "kid")
	if task.AutoRunStatus.String != "dead" || task.AutoRunPID.Valid {
		t.Fatalf("auto run = %q pid %v, want dead with no pid", task.AutoRunStatus.String, task.AutoRunPID)
	}
	ms, _ := flowdb.PendingForTask(db, "parent")
	if len(ms) != 1 || !strings.Contains(ms[0].Body, "ended dead") {
		t.Fatalf("parent notices = %d, want 1 dead-child notice", len(ms))
	}
}

// Item 1b: a failed pid record is no longer just a warning. With neither the
// launcher's record nor the supervisor's own adoption landing, the supervisor
// is stopped and the claim released, so no live run hides behind a row that a
// later reconcile would mark dead (and a second run could claim).
func TestAutoRunUnrecordedLaunchIsStopped(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ur")
	stubITerm(t)
	stubAutoLauncher(t, 9402) // starts nothing: no supervisor adopts the row
	stubRecorderFails(t)
	var killed []int
	oldKill := autoRunKiller
	autoRunKiller = func(pid int) error { killed = append(killed, pid); return nil }
	t.Cleanup(func() { autoRunKiller = oldKill })

	if rc := cmdDo([]string{"ur", "--auto"}); rc != 1 {
		t.Fatalf("cmdDo --auto rc=%d, want 1 (unrecorded launch)", rc)
	}
	if len(killed) != 1 || killed[0] != 9402 {
		t.Fatalf("killed = %v, want the unrecorded supervisor 9402", killed)
	}
	db := openFlowDB(t)
	if task, _ := flowdb.GetTask(db, "ur"); task.AutoRunStatus.String == "running" {
		t.Fatalf("claim left 'running' after a stopped launch")
	}
}

// Item 1b, other half: the record failed but the supervisor adopted its row
// itself, so the run is known and stays.
func TestAutoRunRecordFailureCoveredByAdoption(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "ad")
	stubITerm(t)
	stubRecorderFails(t)
	killed := false
	oldKill := autoRunKiller
	autoRunKiller = func(int) error { killed = true; return nil }
	t.Cleanup(func() { autoRunKiller = oldKill })
	old := autoLauncher
	autoLauncher = func(slug, workDir, logPath, injection string, env []string) (int, error) {
		_, _ = flowdb.AdoptAutoRun(openFlowDB(t), slug, logPath, 9403) // the supervisor's first write
		return 9403, nil
	}
	t.Cleanup(func() { autoLauncher = old })

	if rc := cmdDo([]string{"ad", "--auto"}); rc != 0 {
		t.Fatalf("cmdDo --auto rc=%d, want 0", rc)
	}
	task, _ := flowdb.GetTask(openFlowDB(t), "ad")
	if killed || task.AutoRunStatus.String != "running" || task.AutoRunPID.Int64 != 9403 {
		t.Fatalf("killed=%v run=%q pid=%v, want the adopted run kept", killed, task.AutoRunStatus.String, task.AutoRunPID)
	}
}

func stubRecorderFails(t *testing.T) {
	t.Helper()
	oldRec, oldWait, oldBudget := autoRunRecorder, autoAdoptWait, dbRetryBudget
	autoRunRecorder = func(*sql.DB, string, string, int) (bool, error) { return false, errors.New("disk I/O error") }
	autoAdoptWait, dbRetryBudget = 300*time.Millisecond, 0
	t.Cleanup(func() { autoRunRecorder, autoAdoptWait, dbRetryBudget = oldRec, oldWait, oldBudget })
}

// Item 1: reconcile is keyed on the observed owner. A snapshot taken while the
// claimant's (now dead) pid was on the row must not settle the run once its
// supervisor adopted it.
func TestReconcileSparesAdoptedRun(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "rc")
	db := openFlowDB(t)
	if ok, err := flowdb.ClaimAutoRun(db, "rc", flowdb.RunClaim{LogPath: "/runs/rc.log", HolderPID: 111}, false); err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	snap, _ := flowdb.GetTask(db, "rc")
	if _, err := flowdb.AdoptAutoRun(db, "rc", "/runs/rc.log", 222); err != nil {
		t.Fatal(err)
	}
	stubAlive(t, func(pid int) bool { return pid == 222 })
	reconcileAutoRun(db, snap)
	if task, _ := flowdb.GetTask(db, "rc"); task.AutoRunStatus.String != "running" || task.AutoRunPID.Int64 != 222 {
		t.Fatalf("adopted run settled by a stale reconcile: %q pid %v", task.AutoRunStatus.String, task.AutoRunPID)
	}
}

// Item 5 (review item 2 here): only a child whose auto run is running NOW
// revives its driver. A child with a finished auto run (opened later in a
// tab by a human, say) does not, though its row still has a record.
func TestReviveNeedsChildRunningNow(t *testing.T) {
	db, envs := reviveSetup(t)
	if _, err := db.Exec(`UPDATE tasks SET auto_run_status='completed', auto_run_pid=NULL WHERE slug='c'`); err != nil {
		t.Fatal(err)
	}
	if out := mail(t, db, "fin", "c", "d", false); len(*envs) != 0 {
		t.Fatalf("finished child revived its driver: %q", out)
	}
	if flowdb.WasChildReport(db, "fin") {
		t.Error("mail from a finished child was marked a child report")
	}
}

// flow's dead-child notice still revives: the child's supervisor sends it
// right after finalizing, from the run's own process.
func TestReviveOnDeadChildNoticeFromItsSupervisor(t *testing.T) {
	db, envs := reviveSetup(t)
	if _, err := db.Exec(`UPDATE tasks SET auto_run_status='dead', auto_run_pid=NULL WHERE slug='c'`); err != nil {
		t.Fatal(err)
	}
	t.Setenv(autoRunEnvVar, "c")
	child, _ := flowdb.GetTask(db, "c")
	notifyParentOfDeadChild(db, child)
	if len(*envs) != 1 {
		t.Fatalf("launches = %d, want 1 (dead-child notice from the child's own run)", len(*envs))
	}
}

// Item 3: a revive resumes with the driver's own launch mode; only an
// ordinary auto run always skips permissions.
func TestRevivePermissionMode(t *testing.T) {
	cases := []struct {
		name   string
		mode   string // "" = never recorded (e.g. bound with --here)
		revive bool
		want   bool
	}{
		{"revive, driver launched normally", flowdb.PermissionDefault, true, false},
		{"revive, mode unknown", "", true, false},
		{"revive, driver skipped permissions", flowdb.PermissionBypass, true, true},
		{"ordinary auto run", flowdb.PermissionDefault, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setupFlowRoot(t)
			seedTask(t, "pm")
			seedRunningAuto(t, "pm", 4646)
			db := openFlowDB(t)
			if c.mode != "" {
				_ = flowdb.SetPermissionMode(db, "pm", c.mode)
			}
			if c.revive {
				t.Setenv(reviveEnvVar, "pm")
			}
			var got *bool
			stubAutoRunner(t, func(_ harness.Harness, _, _ string, opts harness.LaunchOpts) error {
				v := opts.SkipPermissions
				got = &v
				return nil
			})
			captureStdout(t, func() { cmdAutoExec(autoExecArgv(t, "pm")) })
			if got == nil || *got != c.want {
				t.Fatalf("SkipPermissions = %v, want %v", got, c.want)
			}
		})
	}
}

// Item 3, recording side: flow do stores how it launched the task's session.
func TestDoRecordsPermissionMode(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "rm")
	stubITerm(t)
	if rc := cmdDo([]string{"rm"}); rc != 0 {
		t.Fatalf("flow do rc=%d", rc)
	}
	db := openFlowDB(t)
	if got := flowdb.PermissionMode(db, "rm"); got != flowdb.PermissionDefault {
		t.Fatalf("mode after a normal launch = %q, want %q", got, flowdb.PermissionDefault)
	}
	if rc := cmdDo([]string{"rm", "--force", "--dangerously-skip-permissions"}); rc != 0 {
		t.Fatalf("flow do --dangerously-skip-permissions rc=%d", rc)
	}
	if got := flowdb.PermissionMode(db, "rm"); got != flowdb.PermissionBypass {
		t.Fatalf("mode after a skip-permissions launch = %q, want %q", got, flowdb.PermissionBypass)
	}
}

// Item 6 (review item 4 here): no answer from the harness's turn check
// (timeout/error: known=false) is doubt, and doubt means live: no revive.
func TestReviveProbeUnknownTurnStateIsLive(t *testing.T) {
	db := wakeSetup(t)
	oldPS := claude.PSRunner
	claude.PSRunner = func() ([]byte, error) { return nil, nil } // no live process
	t.Cleanup(func() { claude.PSRunner = oldPS })
	task, _ := flowdb.GetTask(db, "w")
	live, _ := defaultReviveProbe(task, &fakeWaker{live: false, known: false})
	if !live {
		t.Fatal("unknown turn state counted as exited; want live (no revive)")
	}
}

// Item 7: the run's post-exit idle must not overwrite a human's busy. A
// human who opened the same session with --force during the run recorded
// activity after the run started; it stands.
func TestAutoExecKeepsHumanBusyAfterForce(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "hb")
	seedRunningAuto(t, "hb", 4747)
	db := openFlowDB(t)
	stubAutoRunner(t, func(harness.Harness, string, string, harness.LaunchOpts) error {
		time.Sleep(5 * time.Millisecond)
		_ = flowdb.TouchSessionActivity(db, "hb", time.Now()) // the human's tab, mid-run
		return nil
	})
	captureStdout(t, func() { cmdAutoExec(autoExecArgv(t, "hb")) })
	if st, _ := flowdb.GetSessionState(db, "hb"); st.State != flowdb.SessionBusy {
		t.Fatalf("session state = %q after the run, want the human's busy kept", st.State)
	}
}

// Item 7, enabling half: the headless run's own PreToolUse records no
// activity, so the supervisor can tell a human's apart from it.
func TestPreToolUseInAutoRunRecordsNoActivity(t *testing.T) {
	db := wakeSetup(t)
	stubWaker(t, &fakeWaker{live: false})
	t.Setenv(autoRunEnvVar, "w")
	preToolUseSID(t, "sid-w")
	if st, _ := flowdb.GetSessionState(db, "w"); st.State != "" {
		t.Fatalf("auto run's PreToolUse recorded state %q, want none", st.State)
	}
	t.Setenv(autoRunEnvVar, "")
	preToolUseSID(t, "sid-w")
	if st, _ := flowdb.GetSessionState(db, "w"); st.State != flowdb.SessionBusy {
		t.Fatalf("a human session's PreToolUse must still mark busy, got %q", st.State)
	}
}
