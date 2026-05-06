package zellij

import (
	"errors"
	"slices"
	"testing"
)

// TestSpawnTabBasicNoEnv verifies the two-call argv sequence with no env vars:
//  1) zellij action new-tab --name <title> --cwd <cwd>
//  2) zellij action write-chars " <command>\n"   (leading space for histignorespace)
func TestSpawnTabBasicNoEnv(t *testing.T) {
	var calls [][]string
	old := Runner
	Runner = func(args []string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	t.Cleanup(func() { Runner = old })

	if err := SpawnTab("my-task", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d: %v", len(calls), calls)
	}
	wantNewTab := []string{"action", "new-tab", "--name", "my-task", "--cwd", "/tmp"}
	if !slices.Equal(calls[0], wantNewTab) {
		t.Errorf("call[0] = %v; want %v", calls[0], wantNewTab)
	}
	wantWrite := []string{"action", "write-chars", " echo hi\n"}
	if !slices.Equal(calls[1], wantWrite) {
		t.Errorf("call[1] = %v; want %v", calls[1], wantWrite)
	}
}

// TestSpawnTabEnvVarsSorted verifies env vars are emitted alphabetically,
// each value shell-quoted, all space-separated, before the command. This
// matches the iterm/terminal env-prefix contract exactly.
func TestSpawnTabEnvVarsSorted(t *testing.T) {
	var captured []string
	old := Runner
	Runner = func(args []string) error {
		if len(args) >= 3 && args[1] == "write-chars" {
			captured = append(captured, args[2])
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
	if len(captured) != 1 {
		t.Fatalf("expected 1 write-chars call, got %d", len(captured))
	}
	want := " FLOW_PROJECT='flow' FLOW_TASK='my-task' claude --resume abc\n"
	if captured[0] != want {
		t.Errorf("write-chars line = %q; want %q", captured[0], want)
	}
}

// TestSpawnTabPropagatesNewTabError verifies an error from the new-tab
// call is returned and write-chars is NOT attempted.
func TestSpawnTabPropagatesNewTabError(t *testing.T) {
	calls := 0
	want := errors.New("zellij failed: exit status 1: not in a session")
	old := Runner
	Runner = func(args []string) error {
		calls++
		if len(args) >= 2 && args[1] == "new-tab" {
			return want
		}
		return nil
	}
	t.Cleanup(func() { Runner = old })

	err := SpawnTab("t", "/tmp", "echo hi", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if calls != 1 {
		t.Errorf("expected 1 call (new-tab only), got %d", calls)
	}
	if err.Error() != want.Error() {
		t.Errorf("expected pass-through of new-tab error, got: %v", err)
	}
}

// TestShellQuote — same contract as iterm.ShellQuote / terminal.ShellQuote.
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
