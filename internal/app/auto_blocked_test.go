package app

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/listfmt"
)

// finalAutoRun returns the task's auto-run status after a supervisor ran.
func finalAutoRun(t *testing.T, slug string) *flowdb.Task {
	t.Helper()
	task, err := flowdb.GetTask(openFlowDB(t), slug)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// A child that ends with a `BLOCKED:` line in its log (claude -p prints
// only the final message to stdout, so the log holds it) and exits non-zero
// finalizes as blocked, not dead, and exits 0: the run ended on purpose.
func TestAutoExecFinalizesBlockedFromLogLine(t *testing.T) {
	root := setupFlowRoot(t)
	logPath := seedDeadPingTask(t, root, "stuck")
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(logPath, []byte("flow: started\nBLOCKED: need the staging API token\n"), 0o644); err != nil {
			return err
		}
		return errors.New("exit status 1")
	})

	if rc := cmdAutoExec(autoExecArgv(t, "stuck")); rc != 0 {
		t.Fatalf("rc=%d, want 0 for a blocked run", rc)
	}
	task := finalAutoRun(t, "stuck")
	if task.AutoRunStatus.String != "blocked" {
		t.Errorf("auto_run_status = %q, want blocked", task.AutoRunStatus.String)
	}
	if !task.AutoRunFinished.Valid || task.AutoRunPID.Valid {
		t.Errorf("blocked must set auto_run_finished and clear the pid: finished=%v pid=%v",
			task.AutoRunFinished, task.AutoRunPID)
	}
	// One notice to the human, worded as a block, not a crash.
	rows := deadPingRows(t)
	if len(rows) != 1 || rows[0].toA != "user" || !rows[0].ping {
		t.Fatalf("blocked run notices = %+v, want one pinging human notice", rows)
	}
	if want := "stuck is blocked: need the staging API token; it's waiting on you"; rows[0].body != want {
		t.Errorf("body = %q, want %q", rows[0].body, want)
	}
	if strings.Contains(rows[0].body, "died") {
		t.Errorf("blocked notice reads like a crash: %q", rows[0].body)
	}
}

// When the harness writes the final message to --last-message (codex -o),
// that file is the authority: a BLOCKED line in the log alone is ignored.
func TestAutoExecBlockedFromLastMessageFile(t *testing.T) {
	root := setupFlowRoot(t)
	logPath := seedDeadPingTask(t, root, "stuck")
	lastMsg := autoRunLastMessagePath(logPath)
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		if err := os.MkdirAll(filepath.Dir(lastMsg), 0o755); err != nil {
			return err
		}
		return os.WriteFile(lastMsg, []byte("did part A\n\nBLOCKED: waiting on the DB migration owner\n"), 0o644)
	})

	if rc := cmdAutoExec(autoExecArgv(t, "stuck")); rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
	if got := finalAutoRun(t, "stuck").AutoRunStatus.String; got != "blocked" {
		t.Errorf("auto_run_status = %q, want blocked", got)
	}
}

// The last-message file has no BLOCKED line, so the run is dead even though
// the log holds one: fixtures are written before the run, and any setup
// failure stops the test rather than passing it vacuously.
func TestAutoExecLastMessageFileIsAuthoritative(t *testing.T) {
	root := setupFlowRoot(t)
	logPath := seedDeadPingTask(t, root, "crashy")
	lastMsg := autoRunLastMessagePath(logPath)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("BLOCKED: quoted from a tool result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lastMsg, []byte("crashed mid-way\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		return errors.New("exit status 1")
	})

	cmdAutoExec(autoExecArgv(t, "crashy"))
	if got := finalAutoRun(t, "crashy").AutoRunStatus.String; got != "dead" {
		t.Errorf("auto_run_status = %q, want dead (the last-message file has no BLOCKED line)", got)
	}
}

// A BLOCKED: line followed by a closing line still makes the run blocked:
// the closing line is the usual last line of a final message.
func TestAutoExecBlockedWithClosingLine(t *testing.T) {
	root := setupFlowRoot(t)
	logPath := seedDeadPingTask(t, root, "closing")
	lastMsg := autoRunLastMessagePath(logPath)
	if err := os.MkdirAll(filepath.Dir(lastMsg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lastMsg, []byte("did part A\nBLOCKED: staging access\nI reported to the parent.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		return errors.New("exit status 1")
	})

	cmdAutoExec(autoExecArgv(t, "closing"))
	if got := finalAutoRun(t, "closing").AutoRunStatus.String; got != "blocked" {
		t.Errorf("auto_run_status = %q, want blocked (BLOCKED: on a middle line)", got)
	}
}

// finalMessageHasBlockedLine: a line starting with BLOCKED: (leading
// whitespace ignored) counts; BLOCKED: mid-sentence and a message with no
// such line do not.
func TestFinalMessageHasBlockedLine(t *testing.T) {
	cases := []struct {
		name, body string
		want       bool
	}{
		{"middle line then closing line", "did part A\nBLOCKED: need token\nI reported to X.\n", true},
		{"leading whitespace", "  \tBLOCKED: need token\n", true},
		{"no BLOCKED line", "did everything\nall green\n", false},
		{"BLOCKED: mid-sentence", "the note said BLOCKED: earlier, see above\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "last.txt")
			if err := os.WriteFile(p, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := finalMessageBlockedLine(p) != ""; got != c.want {
				t.Errorf("finalMessageBlockedLine set = %v, want %v", got, c.want)
			}
		})
	}
}

// Only a line that STARTS with BLOCKED: counts; prose that mentions it does not.
func TestAutoExecBlockedMustStartALine(t *testing.T) {
	root := setupFlowRoot(t)
	logPath := seedDeadPingTask(t, root, "prose")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("note: BLOCKED: not a status line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		return errors.New("exit status 1")
	})

	cmdAutoExec(autoExecArgv(t, "prose"))
	if got := finalAutoRun(t, "prose").AutoRunStatus.String; got != "dead" {
		t.Errorf("auto_run_status = %q, want dead", got)
	}
}

// `flow update task --waiting` during the run is a human's note, not the
// run's report: with no BLOCKED: line the run is dead.
func TestAutoExecWaitingOnAloneIsNotBlocked(t *testing.T) {
	root := setupFlowRoot(t)
	seedDeadPingTask(t, root, "waits")
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		_, err := openFlowDB(t).Exec(`UPDATE tasks SET waiting_on='design sign-off from alice' WHERE slug='waits'`)
		return err
	})

	if rc := cmdAutoExec(autoExecArgv(t, "waits")); rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
	if got := finalAutoRun(t, "waits").AutoRunStatus.String; got != "dead" {
		t.Errorf("auto_run_status = %q, want dead", got)
	}
}

// A waiting_on that was already set before the run is not this run's block.
func TestAutoExecPreexistingWaitingOnIsNotBlocked(t *testing.T) {
	root := setupFlowRoot(t)
	seedDeadPingTask(t, root, "old-wait")
	if _, err := openFlowDB(t).Exec(`UPDATE tasks SET waiting_on='set last week' WHERE slug='old-wait'`); err != nil {
		t.Fatal(err)
	}
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		return errors.New("harness exploded")
	})

	cmdAutoExec(autoExecArgv(t, "old-wait"))
	if got := finalAutoRun(t, "old-wait").AutoRunStatus.String; got != "dead" {
		t.Errorf("auto_run_status = %q, want dead", got)
	}
}

// A self-`flow done` wins over any BLOCKED line: the work is finished.
func TestAutoExecDoneWinsOverBlocked(t *testing.T) {
	root := setupFlowRoot(t)
	logPath := seedDeadPingTask(t, root, "finished")
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(logPath, []byte("BLOCKED: stale note from earlier\n"), 0o644); err != nil {
			return err
		}
		_, err := openFlowDB(t).Exec(`UPDATE tasks SET status='done' WHERE slug='finished'`)
		return err
	})

	cmdAutoExec(autoExecArgv(t, "finished"))
	if got := finalAutoRun(t, "finished").AutoRunStatus.String; got != "completed" {
		t.Errorf("auto_run_status = %q, want completed", got)
	}
}

// A blocked finalize whose write fails must not report "finished: blocked":
// the run exits 1 and says the status could not be recorded.
func TestAutoExecBlockedWriteFailureFailsRun(t *testing.T) {
	root := setupFlowRoot(t)
	logPath := seedDeadPingTask(t, root, "nowrite")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("BLOCKED: need access\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		_, err := openFlowDB(t).Exec(`CREATE TRIGGER fail_blocked BEFORE UPDATE ON tasks WHEN NEW.auto_run_status='blocked' BEGIN SELECT RAISE(ABORT,'boom'); END`)
		if err != nil {
			return err
		}
		return errors.New("exit status 1")
	})

	argv := autoExecArgv(t, "nowrite") // before capture: a Fatal here must not leave stdout redirected
	var rc int
	out := captureStdout(t, func() { rc = cmdAutoExec(argv) })
	if rc != 1 {
		t.Errorf("rc=%d, want 1 when the blocked status cannot be recorded", rc)
	}
	if strings.Contains(out, "finished: blocked") {
		t.Errorf("printed a finished: blocked line for an unrecorded run: %q", out)
	}
	// The blocked write was attempted and rejected by the trigger: the run says
	// so, and the row is not stamped blocked. (A trigger that raises rolls back
	// its own side effects, so the rejection is observed in the output.)
	if !strings.Contains(out, "boom") || !strings.Contains(out, "could not be recorded") {
		t.Errorf("no diagnostic that the blocked write was rejected: %q", out)
	}
	if got := finalAutoRun(t, "nowrite").AutoRunStatus.String; got == "blocked" {
		t.Errorf("row recorded blocked despite the failing write")
	}
}

// A retry (or late finalize) from launch A must not overwrite launch B's row:
// the row records B's log, so A's finalize matches nothing.
func TestAutoExecFinalizeIgnoresLaterLaunch(t *testing.T) {
	root := setupFlowRoot(t)
	seedDeadPingTask(t, root, "shared")
	laterLog := filepath.Join(root, "tasks", "shared", "auto-runs", "later.log")
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		// Launch B takes the row while A is still running: B is blocked with no pid.
		_, err := openFlowDB(t).Exec(`UPDATE tasks SET auto_run_log=?, auto_run_status='blocked', auto_run_pid=NULL WHERE slug='shared'`, laterLog)
		if err != nil {
			return err
		}
		return errors.New("exit status 1")
	})

	cmdAutoExec(autoExecArgv(t, "shared"))
	task := finalAutoRun(t, "shared")
	if task.AutoRunStatus.String != "blocked" || task.AutoRunLog.String != laterLog {
		t.Errorf("launch A overwrote launch B: status=%q log=%q", task.AutoRunStatus.String, task.AutoRunLog.String)
	}
}

// Each launch reserves a log no other launch uses, even within one second.
func TestReserveAutoRunLogIsUniquePerLaunch(t *testing.T) {
	dir := t.TempDir()
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		p, err := reserveAutoRunLog(dir)
		if err != nil {
			t.Fatal(err)
		}
		if seen[p] {
			t.Fatalf("launch %d reused log %s", i, p)
		}
		seen[p] = true
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("reserved log not created: %v", err)
		}
	}
}

func TestBlockedRunColorIsYellow(t *testing.T) {
	if got := autoRunColor("blocked"); got != listfmt.Yellow {
		t.Errorf("autoRunColor(blocked) = %q, want yellow", got)
	}
}

// The bootstrap prompt must tell a stuck child how to say so, or the
// BLOCKED: signal is unreachable.
func TestBootstrapPromptAsksForBlockedLine(t *testing.T) {
	p := buildAutoBootstrapPrompt("s", "regular", "", "")
	if !strings.Contains(p, "line starting `BLOCKED:`") {
		t.Errorf("prompt does not instruct the BLOCKED: line:\n%s", p)
	}
}

// sqliteCodeErr is a stand-in for *sqlite.Error: an error carrying an SQLite
// result code.
type sqliteCodeErr int

func (e sqliteCodeErr) Error() string { return "sqlite error code " + strconv.Itoa(int(e)) }
func (e sqliteCodeErr) Code() int     { return int(e) }

// Transient SQLite errors are retried until the shared budget runs out;
// anything else returns on the first call.
func TestRetryTransientDB(t *testing.T) {
	oldBackoff, oldBudget := dbRetryBackoff, dbRetryBudget
	dbRetryBackoff = 0
	generous := 10 * time.Second // success cases return on success, so this never sleeps
	t.Cleanup(func() { dbRetryBackoff, dbRetryBudget = oldBackoff, oldBudget })

	cases := []struct {
		name      string
		budget    time.Duration
		fail      func(call int) error // error for call number `call`; nil = success
		wantErr   bool
		wantCalls int // exact call count; 0 = only assert at least one attempt
	}{
		{"CANTOPEN then success", generous, func(c int) error {
			if c < 3 {
				return sqliteCodeErr(14)
			}
			return nil
		}, false, 3},
		{"LOCKED then success", generous, func(c int) error {
			if c < 2 {
				return sqliteCodeErr(6)
			}
			return nil
		}, false, 2},
		{"extended BUSY code classified by primary code", generous, func(c int) error {
			if c < 2 {
				return sqliteCodeErr(5 | 1<<8)
			}
			return nil
		}, false, 2},
		{"BUSY exhausts the budget", 5 * time.Millisecond, func(int) error { return sqliteCodeErr(5) }, true, 0},
		{"CONSTRAINT is not retried", generous, func(int) error { return sqliteCodeErr(19) }, true, 1},
		{"message text alone is not retried", generous, func(int) error { return errors.New("database is locked") }, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dbRetryBudget = tc.budget
			calls := 0
			err := retryTransientDB(func() error {
				calls++
				return tc.fail(calls)
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantCalls == 0 {
				if calls < 1 {
					t.Errorf("calls=%d, want at least one attempt", calls)
				}
			} else if calls != tc.wantCalls {
				t.Errorf("calls=%d, want %d", calls, tc.wantCalls)
			}
		})
	}
}
