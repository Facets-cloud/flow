package app

import (
	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
	"os"
	"path/filepath"
	"testing"
)

// Re-running an auto on a task whose session already has a transcript must
// resume it (claude rejects --session-id for an existing session); a task
// with no transcript yet still starts a new one.
func TestAutoExecResumesWhenTranscriptExists(t *testing.T) {
	for _, withTranscript := range []bool{false, true} {
		setupFlowRoot(t)
		seedTask(t, "auto-task")
		seedRunningAuto(t, "auto-task", 4242)
		task, err := flowdb.GetTask(openFlowDB(t), "auto-task")
		if err != nil {
			t.Fatal(err)
		}
		if withTranscript {
			home, _ := os.UserHomeDir()
			dir := filepath.Join(home, ".claude", "projects", claude.EncodeCwd(task.WorkDir))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, task.SessionID.String+".jsonl"), []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		var got harness.LaunchOpts
		stubAutoRunner(t, func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
			got = opts
			return nil
		})
		if rc := cmdAutoExec([]string{"auto-task"}); rc != 0 {
			t.Fatalf("cmdAutoExec rc=%d", rc)
		}
		if got.Resume != withTranscript {
			t.Errorf("transcript=%v: opts.Resume=%v", withTranscript, got.Resume)
		}
	}
}
