package app

import (
	"flow/internal/harness"
	"testing"
)

// #120 follow-up (supervisor / `flow done` window). The supervisor reads
// the task status once after the harness exits. A `flow done` that lands
// AFTER that read but while the supervisor pid is still alive is deferred
// to the supervisor (Codex) — so the supervisor must still pick it up:
// sweep once, finalize 'completed', and never send a dead-child notice.
func TestAutoExecDoneAfterStatusReadIsPickedUp(t *testing.T) {
	for _, tc := range []struct {
		harness    string
		wantSweeps int // supervisor-side sweeps (Codex defers; Claude sweeps inline)
	}{
		{"codex", 1},
		{"claude", 1},
	} {
		t.Run(tc.harness, func(t *testing.T) {
			setupFlowRoot(t)
			seedTask(t, "parent")
			seedHarnessTask(t, "cx", tc.harness)
			db := openFlowDB(t)
			if _, err := db.Exec(`UPDATE tasks SET parent_slug='parent' WHERE slug='cx'`); err != nil {
				t.Fatal(err)
			}
			db.Close()
			seedRunningAuto(t, "cx", 4242)
			stubAlive(t, func(pid int) bool { return pid == 4242 })
			codexCalls := stubCodexSweep(t, nil)
			claudeCalls := stubClaudeRunner(t, nil)
			stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
				return nil // harness exits without closing the task
			})
			// The late done: lands right after the supervisor's status read.
			stubAfterAutoRunStatusRead(t, func() {
				t.Setenv(autoRunEnvVar, "") // a human (or a late backgrounded done)
				if rc := cmdDone([]string{"cx"}); rc != 0 {
					t.Fatalf("late done rc=%d", rc)
				}
			})
			captureStdout(t, func() {
				if rc := cmdAutoExec([]string{"cx"}); rc != 0 {
					t.Fatalf("cmdAutoExec rc=%d", rc)
				}
			})

			if got := len(*codexCalls) + len(*claudeCalls); got != tc.wantSweeps {
				t.Errorf("sweeps=%d, want %d", got, tc.wantSweeps)
			}
			got := taskStatus(t, "cx")
			if got.Status != "done" {
				t.Errorf("status=%q, want done", got.Status)
			}
			if got.AutoRunStatus.String != "completed" {
				t.Errorf("auto_run=%q, want completed", got.AutoRunStatus.String)
			}
			msgs, _ := pendingForIdentity(openFlowDB(t), busSender{Assignee: busUser, TaskSlug: "parent"})
			if len(msgs) != 0 {
				t.Errorf("a done child must not be reported dead; parent got %d notice(s)", len(msgs))
			}
		})
	}
}

// The other order: the supervisor finalizes (pid cleared) after `flow
// done` loaded the task but before it flipped the status. The supervisor
// pid is still alive (it is exiting), and the in-memory row still says
// 'running' — but nobody will sweep any more, so done must sweep inline.
func TestDoneAfterSupervisorFinalizedSweepsInline(t *testing.T) {
	setupFlowRoot(t)
	seedHarnessTask(t, "cx", "codex")
	seedRunningAuto(t, "cx", 4242)
	stubAlive(t, func(pid int) bool { return pid == 4242 })
	calls := stubCodexSweep(t, nil)
	t.Setenv(autoRunEnvVar, "")
	stubBeforeDoneFlip(t, func() {
		db := openFlowDB(t)
		defer db.Close()
		if _, err := finalizeAutoRun(db, "cx", "dead"); err != nil {
			t.Fatal(err)
		}
	})
	captureStdout(t, func() {
		if rc := cmdDone([]string{"cx"}); rc != 0 {
			t.Fatalf("done rc=%d", rc)
		}
	})
	if len(*calls) != 1 {
		t.Errorf("inline sweep calls=%d, want 1 (supervisor already finalized)", len(*calls))
	}
}

func stubAfterAutoRunStatusRead(t *testing.T, fn func()) {
	t.Helper()
	old := afterAutoRunStatusRead
	afterAutoRunStatusRead = fn
	t.Cleanup(func() { afterAutoRunStatusRead = old })
}

func stubBeforeDoneFlip(t *testing.T, fn func()) {
	t.Helper()
	old := beforeDoneFlip
	beforeDoneFlip = fn
	t.Cleanup(func() { beforeDoneFlip = old })
}
