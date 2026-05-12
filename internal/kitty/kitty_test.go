package kitty

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestSpawnTabBasicNoEnv verifies the two-call argv sequence with no
// env vars:
//  1. RunnerOutput: kitty @ launch --type=tab --tab-title=<t> --cwd=<cwd>  (returns window id)
//  2. Runner:       kitty @ send-text --match=id:<id> " <command>\n"      (leading space for histignorespace)
func TestSpawnTabBasicNoEnv(t *testing.T) {
	var launchArgs []string
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		launchArgs = append([]string(nil), args...)
		return []byte("42\n"), nil
	})

	var sendArgs []string
	old := Runner
	Runner = func(args []string) error {
		sendArgs = append([]string(nil), args...)
		return nil
	}
	t.Cleanup(func() { Runner = old })

	if err := SpawnTab("my-task", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}

	wantLaunch := []string{"@", "launch", "--type=tab", "--tab-title=my-task", "--cwd=/tmp"}
	if !slices.Equal(launchArgs, wantLaunch) {
		t.Errorf("launch argv = %v; want %v", launchArgs, wantLaunch)
	}
	wantSend := []string{"@", "send-text", "--match=id:42", " echo hi\n"}
	if !slices.Equal(sendArgs, wantSend) {
		t.Errorf("send argv = %v; want %v", sendArgs, wantSend)
	}
}

// TestSpawnTabEnvVarsSorted verifies env vars are emitted alphabetically,
// each value shell-quoted, all space-separated, before the command. This
// matches the iterm/terminal/zellij env-prefix contract exactly.
func TestSpawnTabEnvVarsSorted(t *testing.T) {
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return []byte("17\n"), nil
	})

	var captured string
	old := Runner
	Runner = func(args []string) error {
		if len(args) >= 4 && args[1] == "send-text" {
			captured = args[3]
		}
		return nil
	}
	t.Cleanup(func() { Runner = old })

	envVars := map[string]string{
		"FLOW_TASK":    "my-task",
		"FLOW_PROJECT": "flow",
	}
	if err := SpawnTab("flow/my-task", "/Users/me/repo", "claude --resume abc", envVars); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	want := " FLOW_PROJECT='flow' FLOW_TASK='my-task' claude --resume abc\n"
	if captured != want {
		t.Errorf("send-text payload = %q; want %q", captured, want)
	}
}

// TestSpawnTabLaunchErrorShortCircuits — an error from `kitty @ launch`
// must be wrapped (with the remote-control hint) and send-text must NOT
// be attempted.
func TestSpawnTabLaunchErrorShortCircuits(t *testing.T) {
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return nil, errors.New("exit status 1: remote control is not enabled")
	})
	runnerCalled := false
	old := Runner
	Runner = func(args []string) error {
		runnerCalled = true
		return nil
	}
	t.Cleanup(func() { Runner = old })

	err := SpawnTab("t", "/tmp", "echo hi", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if runnerCalled {
		t.Error("Runner (send-text) should not be called when launch fails")
	}
	if !strings.Contains(err.Error(), "allow_remote_control") {
		t.Errorf("expected error to mention remote_control hint, got: %v", err)
	}
}

// TestSpawnTabLaunchEmptyOutput — if kitty @ launch returns empty
// stdout, SpawnTab must fail rather than send-text to id:"" (which
// kitty would interpret as match-all).
func TestSpawnTabLaunchEmptyOutput(t *testing.T) {
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return []byte("\n"), nil
	})
	runnerCalled := false
	old := Runner
	Runner = func(args []string) error {
		runnerCalled = true
		return nil
	}
	t.Cleanup(func() { Runner = old })

	err := SpawnTab("t", "/tmp", "echo hi", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if runnerCalled {
		t.Error("Runner (send-text) should not be called when launch returns empty id")
	}
}

// TestSpawnTabFlattensEmbeddedNewlines — embedded `\n` in command must
// be replaced with a space before send-text, same reasoning as zellij
// (PTY would interpret each \n as Enter and run partial lines).
func TestSpawnTabFlattensEmbeddedNewlines(t *testing.T) {
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return []byte("5\n"), nil
	})
	var captured string
	old := Runner
	Runner = func(args []string) error {
		if len(args) >= 4 && args[1] == "send-text" {
			captured = args[3]
		}
		return nil
	}
	t.Cleanup(func() { Runner = old })

	cmd := "claude --session-id abc 'line1\nline2\nline3'"
	if err := SpawnTab("t", "/tmp", cmd, nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	want := " claude --session-id abc 'line1 line2 line3'\n"
	if captured != want {
		t.Errorf("send-text payload = %q; want %q", captured, want)
	}
	if got := strings.Count(captured, "\n"); got != 1 {
		t.Errorf("send-text payload contains %d newlines; want exactly 1", got)
	}
}

func stubRunnerOutput(t *testing.T, fn func([]string) ([]byte, error)) {
	t.Helper()
	old := RunnerOutput
	RunnerOutput = fn
	t.Cleanup(func() { RunnerOutput = old })
}

// TestShellQuote — same contract as iterm/terminal/zellij.
func TestShellQuote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain", "'plain'"},
		{"with space", "'with space'"},
		{"with'quote", `'with'\''quote'`},
	}
	for _, tc := range cases {
		if got := ShellQuote(tc.in); got != tc.want {
			t.Errorf("ShellQuote(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
