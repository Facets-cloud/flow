package app

import (
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/codex"
)

// $FLOW_TERM=bg on a Codex task spawns the Codex TUI in a detached tmux
// session and binds the task to the minted thread (no tab opened).
func TestCmdDoBackgroundCodexTmux(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "cx-bg")
	stubBGMode(t)
	itermCount, _ := stubITerm(t)
	const sid = "019ff4b1-6162-7263-974f-f5f1866ad0fe"
	stubCodexProbe(t, sid)
	var newSession []string
	old := codex.TmuxRunner
	codex.TmuxRunner = func(args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "new-session" {
			newSession = args
		}
		return nil, nil
	}
	t.Cleanup(func() { codex.TmuxRunner = old })
	stubCodexTmuxPresent(t)

	var rc int
	out := captureStdout(t, func() { rc = cmdDo([]string{"cx-bg", "--harness", "codex", "--dangerously-skip-permissions"}) })
	if rc != 0 {
		t.Fatalf("rc=%d out=%s", rc, out)
	}
	if *itermCount != 0 {
		t.Errorf("opened %d tabs, want 0", *itermCount)
	}
	if newSession == nil || !strings.Contains(newSession[len(newSession)-1], "resume "+sid) {
		t.Fatalf("tmux new-session = %v", newSession)
	}
	if !strings.Contains(out, "tmux attach -t flow-codex-"+sid) {
		t.Errorf("output should name the attach command: %s", out)
	}
	task, _ := flowdb.GetTask(openFlowDB(t), "cx-bg")
	if task.Harness.String != "codex" || task.SessionID.String != sid || task.Status != "in-progress" {
		t.Errorf("task = %q/%q/%q", task.Harness.String, task.SessionID.String, task.Status)
	}
}

func TestBgHintsForCodex(t *testing.T) {
	h := codex.New()
	const sid = "019ff4b1-6162-7263-974f-f5f1866ad0fe"
	if got := bgViewHint(h, sid); !strings.Contains(got, "tmux attach -t flow-codex-"+sid) {
		t.Errorf("view hint = %q", got)
	}
	if got := bgAttachHint(h, &harness.BackgroundAgent{SessionID: sid}); got != "tmux attach -t flow-codex-"+sid {
		t.Errorf("attach hint = %q", got)
	}
}

// stubCodexTmuxPresent makes the tmux binary probe succeed.
func stubCodexTmuxPresent(t *testing.T) {
	t.Helper()
	old := codex.TmuxLookPath
	codex.TmuxLookPath = func(string) (string, error) { return "/usr/bin/tmux", nil }
	t.Cleanup(func() { codex.TmuxLookPath = old })
}
