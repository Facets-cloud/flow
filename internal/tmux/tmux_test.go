package tmux

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// stubTmux replaces Runner and RunnerOutput with recorders. new-window
// returns paneID; every other call succeeds.
func stubTmux(t *testing.T, paneID string) *[][]string {
	t.Helper()
	var calls [][]string
	oldR, oldRO := Runner, RunnerOutput
	Runner = func(args []string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	RunnerOutput = func(args []string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte(paneID + "\n"), nil
	}
	t.Cleanup(func() { Runner, RunnerOutput = oldR, oldRO })
	return &calls
}

// TestSpawnTabBasicNoEnv verifies the three-call argv sequence:
//  1. tmux new-window -P -F '#{pane_id}' -n <title> -c <cwd>   (prints pane id)
//  2. tmux send-keys -t <pane> -l " <command>"                (literal, histignorespace)
//  3. tmux send-keys -t <pane> Enter
func TestSpawnTabBasicNoEnv(t *testing.T) {
	calls := stubTmux(t, "%7")
	if err := SpawnTab("my-task", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	want := [][]string{
		{"new-window", "-P", "-F", "#{pane_id}", "-n", "my-task", "-c", "/tmp"},
		{"send-keys", "-t", "%7", "-l", " echo hi"},
		{"send-keys", "-t", "%7", "Enter"},
	}
	if len(*calls) != len(want) {
		t.Fatalf("calls = %v; want %v", *calls, want)
	}
	for i := range want {
		if !slices.Equal((*calls)[i], want[i]) {
			t.Errorf("call[%d] = %v; want %v", i, (*calls)[i], want[i])
		}
	}
}

// TestSpawnTabEnvVarsSorted — env vars are emitted alphabetically, each
// value shell-quoted, before the command (same contract as every other
// backend).
func TestSpawnTabEnvVarsSorted(t *testing.T) {
	calls := stubTmux(t, "%1")
	env := map[string]string{"FLOW_ROOT": "/home/me/.flow", "AAA": "it's"}
	if err := SpawnTab("t", "/repo", "claude --resume abc", env); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	got := (*calls)[1][4]
	want := ` AAA='it'\''s' FLOW_ROOT='/home/me/.flow' claude --resume abc`
	if got != want {
		t.Errorf("literal line = %q; want %q", got, want)
	}
}

// TestSpawnTabFlattensEmbeddedNewlines — send-keys -l types raw bytes,
// so embedded newlines would submit partial lines. They become spaces.
func TestSpawnTabFlattensEmbeddedNewlines(t *testing.T) {
	calls := stubTmux(t, "%1")
	if err := SpawnTab("t", "/tmp", "claude --session-id abc 'l1\nl2\nl3'", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if got, want := (*calls)[1][4], " claude --session-id abc 'l1 l2 l3'"; got != want {
		t.Errorf("literal line = %q; want %q", got, want)
	}
}

// TestSpawnTabPropagatesNewWindowError — a failing new-window returns
// the error and no send-keys is attempted.
func TestSpawnTabPropagatesNewWindowError(t *testing.T) {
	var sends int
	oldR, oldRO := Runner, RunnerOutput
	Runner = func(args []string) error { sends++; return nil }
	RunnerOutput = func(args []string) ([]byte, error) {
		return nil, errors.New("no server running")
	}
	t.Cleanup(func() { Runner, RunnerOutput = oldR, oldRO })

	err := SpawnTab("t", "/tmp", "echo hi", nil)
	if err == nil || !strings.Contains(err.Error(), "no server running") {
		t.Fatalf("expected wrapped new-window error, got %v", err)
	}
	if sends != 0 {
		t.Errorf("send-keys should not run after new-window failed; ran %d times", sends)
	}
}

// TestSpawnTabEmptyPaneID — an empty pane id from new-window is an
// error rather than a send-keys into "whatever is focused".
func TestSpawnTabEmptyPaneID(t *testing.T) {
	stubTmux(t, "")
	if err := SpawnTab("t", "/tmp", "echo hi", nil); err == nil {
		t.Fatal("expected error for empty pane id")
	}
}

// TestFocusSessionFound — ps shows the harness on pts/3, list-panes maps
// /dev/pts/3 to pane %4, so select-window + select-pane target %4.
func TestFocusSessionFound(t *testing.T) {
	const sid = "11111111-2222-4333-8444-555555555555"
	var calls [][]string
	oldR, oldRO, oldPS := Runner, RunnerOutput, PSRunner
	Runner = func(args []string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	RunnerOutput = func(args []string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte("%2 /dev/pts/1\n%4 /dev/pts/3\n"), nil
	}
	PSRunner = func() ([]byte, error) {
		return []byte("  PID TTY      COMMAND\n" +
			"  100 pts/1    -bash\n" +
			"  200 pts/3    claude --session-id " + sid + " 'prompt'\n"), nil
	}
	t.Cleanup(func() { Runner, RunnerOutput, PSRunner = oldR, oldRO, oldPS })

	ok, err := FocusSession(sid, "claude")
	if err != nil || !ok {
		t.Fatalf("FocusSession = (%v, %v), want (true, nil)", ok, err)
	}
	want := [][]string{
		{"list-panes", "-a", "-F", "#{pane_id} #{pane_tty}"},
		{"select-window", "-t", "%4"},
		{"select-pane", "-t", "%4"},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v; want %v", calls, want)
	}
	for i := range want {
		if !slices.Equal(calls[i], want[i]) {
			t.Errorf("call[%d] = %v; want %v", i, calls[i], want[i])
		}
	}
}

// TestFocusSessionMiss — no matching process means (false, nil) and no
// tmux call at all.
func TestFocusSessionMiss(t *testing.T) {
	var tmuxCalls int
	oldR, oldRO, oldPS := Runner, RunnerOutput, PSRunner
	Runner = func(args []string) error { tmuxCalls++; return nil }
	RunnerOutput = func(args []string) ([]byte, error) { tmuxCalls++; return []byte(""), nil }
	PSRunner = func() ([]byte, error) { return []byte("  PID TTY COMMAND\n 1 pts/0 -zsh\n"), nil }
	t.Cleanup(func() { Runner, RunnerOutput, PSRunner = oldR, oldRO, oldPS })

	ok, err := FocusSession("11111111-2222-4333-8444-555555555555", "claude")
	if ok || err != nil {
		t.Fatalf("FocusSession = (%v, %v), want (false, nil)", ok, err)
	}
	if tmuxCalls != 0 {
		t.Errorf("tmux should not be invoked without a ps match; got %d calls", tmuxCalls)
	}
}

// TestFocusSessionEmptyID short-circuits.
func TestFocusSessionEmptyID(t *testing.T) {
	oldPS := PSRunner
	PSRunner = func() ([]byte, error) { t.Fatal("ps must not run for empty session id"); return nil, nil }
	t.Cleanup(func() { PSRunner = oldPS })
	if ok, err := FocusSession("", "claude"); ok || err != nil {
		t.Fatalf("got (%v, %v), want (false, nil)", ok, err)
	}
}
