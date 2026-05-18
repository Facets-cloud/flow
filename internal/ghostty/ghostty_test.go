package ghostty

import (
	"strings"
	"testing"
)

// TestSpawnTabScriptShape verifies the AppleScript emitted to osascript
// targets Ghostty, embeds env-var assignments before the command,
// sets the tab title via an OSC 2 escape sequence (Ghostty's `name`
// property is read-only), and uses `new tab in front window` when a
// window already exists. The osascript binary is mocked via Runner.
func TestSpawnTabScriptShape(t *testing.T) {
	var captured string
	old := Runner
	Runner = func(args []string) error {
		if len(args) >= 2 {
			captured = args[1]
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

	mustContain := []string{
		`tell application "Ghostty"`,
		`activate`,
		`if (count of windows) is 0 then`,
		`set newWin to (new window)`,
		`set targetTerm to focused terminal of (first tab of newWin)`,
		`set newTab to (new tab in front window)`,
		`input text "`,
		` & return to targetTerm`,
		// OSC 2 title set inline via printf — Ghostty's name property is read-only.
		`printf '\\033]2;%s\\007' 'flow/my-task' ;`,
		// env vars assigned alphabetically, before the command, all on one line:
		`FLOW_PROJECT='flow' FLOW_TASK='my-task' claude --resume abc`,
		// cd is the first thing in the typed line, single-leading-space
		// for histignorespace:
		` cd '/Users/me/repo' && `,
	}
	for _, s := range mustContain {
		if !strings.Contains(captured, s) {
			t.Errorf("script missing %q\n--- script ---\n%s", s, captured)
		}
	}
}

// TestSpawnTabNoEnvVars covers the env-prefix branch when envVars is
// nil — the line should still cd and run the command, just with no
// VAR=value assignments in front.
func TestSpawnTabNoEnvVars(t *testing.T) {
	var captured string
	old := Runner
	Runner = func(args []string) error {
		if len(args) >= 2 {
			captured = args[1]
		}
		return nil
	}
	t.Cleanup(func() { Runner = old })

	if err := SpawnTab("t", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if !strings.Contains(captured, ` cd '/tmp' && echo hi`) {
		t.Errorf("expected bare `cd … && echo hi` line; got:\n%s", captured)
	}
	if strings.Contains(captured, "echo hi=") {
		t.Errorf("unexpected env assignment in command line: %s", captured)
	}
}

// TestShellQuote is a sanity check on the local helper — same contract
// as iterm.ShellQuote and terminal.ShellQuote.
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
