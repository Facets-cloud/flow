package zellij

import (
	"errors"
	"slices"
	"strings"
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

// TestSpawnTabFlattensEmbeddedNewlines verifies that any `\n` inside
// `command` is replaced with a space before write-chars. Without this,
// zellij types each line into the PTY as a separate Enter-terminated
// command, which breaks the bootstrap prompt (a multi-line numbered
// list) — the shell ends up trying to execute "1. Invoke...",
// "2. Run...", etc. as commands.
func TestSpawnTabFlattensEmbeddedNewlines(t *testing.T) {
	var captured []string
	old := Runner
	Runner = func(args []string) error {
		if len(args) >= 3 && args[1] == "write-chars" {
			captured = append(captured, args[2])
		}
		return nil
	}
	t.Cleanup(func() { Runner = old })

	cmd := "claude --session-id abc 'line1\nline2\nline3'"
	if err := SpawnTab("t", "/tmp", cmd, nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if len(captured) != 1 {
		t.Fatalf("expected 1 write-chars call, got %d", len(captured))
	}
	want := " claude --session-id abc 'line1 line2 line3'\n"
	if captured[0] != want {
		t.Errorf("write-chars line = %q; want %q", captured[0], want)
	}
	// Only one newline (the line-submit terminator) should be present.
	if got := strings.Count(captured[0], "\n"); got != 1 {
		t.Errorf("write-chars line contains %d newlines; want exactly 1", got)
	}
}

// TestFocusSessionEmptyID short-circuits without invoking zellij.
func TestFocusSessionEmptyID(t *testing.T) {
	rCalled := false
	old := RunnerOutput
	RunnerOutput = func(args []string) ([]byte, error) {
		rCalled = true
		return nil, nil
	}
	t.Cleanup(func() { RunnerOutput = old })

	focused, err := FocusSession("")
	if focused || err != nil {
		t.Errorf("FocusSession(\"\") = (%v, %v); want (false, nil)", focused, err)
	}
	if rCalled {
		t.Error("RunnerOutput should not be called for empty session id")
	}
}

// TestFocusSessionMatchesAndFocuses verifies the happy path: list-panes
// JSON contains a non-plugin pane whose pane_command runs claude with
// the target UUID, and FocusSession invokes focus-pane-id with the
// matching pane id.
func TestFocusSessionMatchesAndFocuses(t *testing.T) {
	const uuid = "c18a6fe7-7cb0-4875-93d5-6ad1e9785763"
	const listJSON = `[
  {"id": 1, "is_plugin": true, "pane_command": null, "title": "tab-bar"},
  {"id": 2, "is_plugin": false, "pane_command": "/opt/homebrew/bin/fish", "title": "shell"},
  {"id": 16, "is_plugin": false, "pane_command": "claude --session-id c18a6fe7-7cb0-4875-93d5-6ad1e9785763 You are the execution session", "title": "task-x"}
]`
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		want := []string{"action", "list-panes", "--all", "--json"}
		for i, w := range want {
			if i >= len(args) || args[i] != w {
				t.Errorf("RunnerOutput args = %v; want prefix %v", args, want)
			}
		}
		return []byte(listJSON), nil
	})

	var focusArgs []string
	old := Runner
	Runner = func(args []string) error {
		focusArgs = args
		return nil
	}
	t.Cleanup(func() { Runner = old })

	focused, err := FocusSession(uuid)
	if err != nil {
		t.Fatalf("FocusSession: %v", err)
	}
	if !focused {
		t.Fatal("expected focused=true")
	}
	want := []string{"action", "focus-pane-id", "terminal_16"}
	if !slices.Equal(focusArgs, want) {
		t.Errorf("focus argv = %v; want %v", focusArgs, want)
	}
}

// TestFocusSessionResumeFlag — covers --resume in pane_command.
func TestFocusSessionResumeFlag(t *testing.T) {
	const uuid = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	const listJSON = `[
  {"id": 7, "is_plugin": false, "pane_command": "claude --resume aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}
]`
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte(listJSON), nil })
	var focusArgs []string
	old := Runner
	Runner = func(args []string) error { focusArgs = args; return nil }
	t.Cleanup(func() { Runner = old })

	focused, err := FocusSession(uuid)
	if err != nil || !focused {
		t.Fatalf("got (%v, %v); want (true, nil)", focused, err)
	}
	want := []string{"action", "focus-pane-id", "terminal_7"}
	if !slices.Equal(focusArgs, want) {
		t.Errorf("focus argv = %v; want %v", focusArgs, want)
	}
}

// TestFocusSessionUUIDCaseInsensitive — UUID match is case-insensitive.
func TestFocusSessionUUIDCaseInsensitive(t *testing.T) {
	const uuid = "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE"
	const listJSON = `[
  {"id": 9, "is_plugin": false, "pane_command": "claude --session-id aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}
]`
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte(listJSON), nil })
	old := Runner
	Runner = func(args []string) error { return nil }
	t.Cleanup(func() { Runner = old })

	focused, err := FocusSession(uuid)
	if err != nil || !focused {
		t.Errorf("uppercase UUID should match lowercase pane_command; got (%v, %v)", focused, err)
	}
}

// TestFocusSessionNoMatch — list-panes contains no claude with this UUID.
func TestFocusSessionNoMatch(t *testing.T) {
	const listJSON = `[
  {"id": 2, "is_plugin": false, "pane_command": "/opt/homebrew/bin/fish"},
  {"id": 18, "is_plugin": false, "pane_command": "claude"}
]`
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte(listJSON), nil })
	rCalled := false
	old := Runner
	Runner = func(args []string) error { rCalled = true; return nil }
	t.Cleanup(func() { Runner = old })

	focused, err := FocusSession("11111111-2222-4333-8444-555555555555")
	if focused || err != nil {
		t.Errorf("got (%v, %v); want (false, nil)", focused, err)
	}
	if rCalled {
		t.Error("Runner should not be called when no pane matches")
	}
}

// TestFocusSessionSkipsPluginPanes — plugin panes (tab-bar, status-bar)
// have null pane_command and must not match.
func TestFocusSessionSkipsPluginPanes(t *testing.T) {
	const uuid = "11111111-2222-4333-8444-555555555555"
	// Plugin pane has the UUID in pane_command — pathological but worth
	// guarding against. is_plugin must filter it out.
	const listJSON = `[
  {"id": 2, "is_plugin": true, "pane_command": "claude --session-id 11111111-2222-4333-8444-555555555555"}
]`
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte(listJSON), nil })
	rCalled := false
	old := Runner
	Runner = func(args []string) error { rCalled = true; return nil }
	t.Cleanup(func() { Runner = old })

	focused, err := FocusSession(uuid)
	if focused || err != nil {
		t.Errorf("got (%v, %v); want (false, nil) for plugin pane", focused, err)
	}
	if rCalled {
		t.Error("Runner should not be called when only plugin matched")
	}
}

// TestFocusSessionListPanesError surfaces zellij CLI errors.
func TestFocusSessionListPanesError(t *testing.T) {
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return nil, errors.New("zellij not in a session")
	})
	focused, err := FocusSession("11111111-2222-4333-8444-555555555555")
	if focused || err == nil {
		t.Errorf("got (%v, %v); want (false, non-nil)", focused, err)
	}
}

// TestFocusSessionFocusError surfaces focus-pane-id errors as a backend
// failure (distinct from "no match found").
func TestFocusSessionFocusError(t *testing.T) {
	const uuid = "11111111-2222-4333-8444-555555555555"
	const listJSON = `[
  {"id": 5, "is_plugin": false, "pane_command": "claude --session-id 11111111-2222-4333-8444-555555555555"}
]`
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte(listJSON), nil })
	old := Runner
	Runner = func(args []string) error { return errors.New("zellij failed") }
	t.Cleanup(func() { Runner = old })

	focused, err := FocusSession(uuid)
	if focused || err == nil {
		t.Errorf("got (%v, %v); want (false, non-nil)", focused, err)
	}
}

// TestFocusSessionMalformedJSON — protect against zellij output drift.
func TestFocusSessionMalformedJSON(t *testing.T) {
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte("{not json"), nil })
	focused, err := FocusSession("11111111-2222-4333-8444-555555555555")
	if focused || err == nil {
		t.Errorf("got (%v, %v); want (false, non-nil) for malformed JSON", focused, err)
	}
}

func stubRunnerOutput(t *testing.T, fn func([]string) ([]byte, error)) {
	t.Helper()
	old := RunnerOutput
	RunnerOutput = fn
	t.Cleanup(func() { RunnerOutput = old })
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
