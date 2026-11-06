package app

import (
	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/codex"
	"strings"
	"testing"
)

// stubCodexSweep replaces codex.SkipPermissionsRunner with a recorder.
// onCall (optional) runs inside the stub so a test can observe DB state
// at the moment the sweep executes.
func stubCodexSweep(t *testing.T, onCall func()) *[]string {
	t.Helper()
	old := codex.SkipPermissionsRunner
	calls := &[]string{}
	codex.SkipPermissionsRunner = func(prompt string) error {
		*calls = append(*calls, prompt)
		if onCall != nil {
			onCall()
		}
		return nil
	}
	t.Cleanup(func() { codex.SkipPermissionsRunner = old })
	return calls
}

// seedHarnessTask creates an in-progress task with a session and the
// given harness pin.
func seedHarnessTask(t *testing.T, slug, h string) {
	t.Helper()
	seedTask(t, slug)
	db := openFlowDB(t)
	defer db.Close()
	if _, err := db.Exec(
		`UPDATE tasks SET status='in-progress', session_id=?, session_started=?, harness=? WHERE slug=?`,
		fakeSessionID(slug), flowdb.NowISO(), h, slug,
	); err != nil {
		t.Fatal(err)
	}
}

func taskStatus(t *testing.T, slug string) *flowdb.Task {
	t.Helper()
	db := openFlowDB(t)
	defer db.Close()
	task, err := flowdb.GetTask(db, slug)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// (a) A Codex auto run's own `flow done` must not nest a `codex exec`
// sweep inside the still-running outer Codex process.
func TestDoneCodexAutoRunOwnTaskDefersSweep(t *testing.T) {
	setupFlowRoot(t)
	seedHarnessTask(t, "cx", "codex")
	calls := stubCodexSweep(t, nil)
	t.Setenv(autoRunEnvVar, "cx")

	out := captureStdout(t, func() {
		if rc := cmdDone([]string{"cx"}); rc != 0 {
			t.Fatalf("done rc=%d", rc)
		}
	})
	if len(*calls) != 0 {
		t.Errorf("inline sweep ran %d times, want 0 (deferred)", len(*calls))
	}
	if !strings.Contains(out, "deferred") {
		t.Errorf("stdout should say the sweep is deferred; got %q", out)
	}
	if taskStatus(t, "cx").Status != "done" {
		t.Error("status flip must still happen")
	}
}

// (d) Interactive done (no FLOW_AUTO_RUN) and done of a DIFFERENT task
// from inside an auto run keep sweeping inline, even for Codex.
func TestDoneCodexSweepsInlineOutsideOwnAutoRun(t *testing.T) {
	for _, tc := range []struct{ name, env string }{
		{"interactive", ""},
		{"other-task", "someone-else"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupFlowRoot(t)
			seedHarnessTask(t, "cx", "codex")
			calls := stubCodexSweep(t, nil)
			t.Setenv(autoRunEnvVar, tc.env)
			if rc := cmdDone([]string{"cx"}); rc != 0 {
				t.Fatalf("done rc=%d", rc)
			}
			if len(*calls) != 1 {
				t.Errorf("inline sweep calls=%d, want 1", len(*calls))
			}
		})
	}
}

// (e) Claude auto runs are unchanged: the sweep stays inline.
func TestDoneClaudeAutoRunStillSweepsInline(t *testing.T) {
	setupFlowRoot(t)
	seedHarnessTask(t, "cl", "claude")
	calls := stubClaudeRunner(t, nil)
	t.Setenv(autoRunEnvVar, "cl")
	if rc := cmdDone([]string{"cl"}); rc != 0 {
		t.Fatalf("done rc=%d", rc)
	}
	if len(*calls) != 1 {
		t.Errorf("claude inline sweep calls=%d, want 1", len(*calls))
	}
}

// (b) The supervisor runs the deferred sweep after the harness exits when
// the task is done. auto_run stays 'running' during the sweep and flips to
// 'completed' only after it.
func TestAutoExecCodexRunsDeferredSweepAfterExit(t *testing.T) {
	setupFlowRoot(t)
	seedHarnessTask(t, "cx", "codex")
	seedRunningAuto(t, "cx", 4242)
	var statusDuringSweep string
	calls := stubCodexSweep(t, func() {
		statusDuringSweep = taskStatus(t, "cx").AutoRunStatus.String
	})
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		// The run's own `flow done` (deferred sweep).
		t.Setenv(autoRunEnvVar, "cx")
		if rc := cmdDone([]string{"cx"}); rc != 0 {
			t.Fatalf("inner done rc=%d", rc)
		}
		if len(*calls) != 0 {
			t.Fatalf("sweep ran while harness alive (%d calls)", len(*calls))
		}
		return nil
	})

	out := captureStdout(t, func() {
		if rc := cmdAutoExec([]string{"cx"}); rc != 0 {
			t.Fatalf("cmdAutoExec rc=%d", rc)
		}
	})
	if len(*calls) != 1 {
		t.Fatalf("deferred sweep calls=%d, want 1", len(*calls))
	}
	if !strings.Contains((*calls)[0], `"cx"`) {
		t.Errorf("sweep prompt should name the task; got %q", (*calls)[0][:80])
	}
	if statusDuringSweep != "running" {
		t.Errorf("auto_run during sweep=%q, want running", statusDuringSweep)
	}
	if got := taskStatus(t, "cx").AutoRunStatus.String; got != "completed" {
		t.Errorf("auto_run after=%q, want completed", got)
	}
	if !strings.Contains(out, "close-out sweep") {
		t.Errorf("auto-run log should record the sweep outcome; got %q", out)
	}
}

// (c) No sweep when the run exits without marking the task done.
func TestAutoExecCodexNoSweepWhenNotDone(t *testing.T) {
	setupFlowRoot(t)
	seedHarnessTask(t, "cx", "codex")
	seedRunningAuto(t, "cx", 4242)
	calls := stubCodexSweep(t, nil)
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		return nil
	})
	if rc := cmdAutoExec([]string{"cx"}); rc != 0 {
		t.Fatalf("cmdAutoExec rc=%d", rc)
	}
	if len(*calls) != 0 {
		t.Errorf("sweep calls=%d, want 0 when not done", len(*calls))
	}
	if got := taskStatus(t, "cx").AutoRunStatus.String; got != "dead" {
		t.Errorf("auto_run=%q, want dead", got)
	}
}

// (e) The Claude supervisor never runs a second sweep: Claude's own
// `flow done` already swept inline.
func TestAutoExecClaudeDoesNotSweep(t *testing.T) {
	setupFlowRoot(t)
	seedHarnessTask(t, "cl", "claude")
	seedRunningAuto(t, "cl", 4242)
	calls := stubClaudeRunner(t, nil)
	stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
		db := openFlowDB(t)
		defer db.Close()
		_, err := db.Exec(`UPDATE tasks SET status='done' WHERE slug='cl'`)
		return err
	})
	if rc := cmdAutoExec([]string{"cl"}); rc != 0 {
		t.Fatalf("cmdAutoExec rc=%d", rc)
	}
	if len(*calls) != 0 {
		t.Errorf("claude supervisor sweep calls=%d, want 0", len(*calls))
	}
}
