package terminal

import (
	"errors"
	"strings"
	"testing"
)

// TestSpawnTabScriptShape verifies the AppleScript emitted to osascript
// targets Terminal.app, embeds env-var assignments before the command,
// uses cmd-T via System Events for the new-tab path, and falls back to
// `do script` (no `in` clause) when no windows exist. The osascript
// binary is mocked via Runner.
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
		`tell application "Terminal"`,
		`activate`,
		`if (count of windows) is 0 then`,
		`do script "`,
		`tell application "System Events"`,
		`keystroke "t" using {command down}`,
		`set custom title of newTab to "flow/my-task"`,
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
	if strings.Contains(captured, "=") && strings.Contains(captured, "echo hi=") {
		t.Errorf("unexpected env assignment in command line: %s", captured)
	}
}

// TestSpawnTabWrapsAccessibilityError verifies that when osascript
// fails with a macOS Accessibility-denied error pattern, SpawnTab
// returns a wrapped error explaining what's missing and how to fix
// it. The Claude session that called `flow do` relies on this
// message verbatim to walk the user through System Settings.
func TestSpawnTabWrapsAccessibilityError(t *testing.T) {
	cases := []struct{ name, stderr string }{
		{"keystrokes-denied",
			"osascript failed: exit status 1: System Events got an error: osascript is not allowed to send keystrokes. (-1002)"},
		{"assistive-access",
			"osascript failed: exit status 1: not allowed assistive access (-25211)"},
		{"apple-events",
			"osascript failed: exit status 1: not authorized to send Apple events to System Events. (-1743)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := Runner
			Runner = func(args []string) error { return errors.New(tc.stderr) }
			t.Cleanup(func() { Runner = old })

			err := SpawnTab("t", "/tmp", "echo hi", nil)
			if err == nil {
				t.Fatal("expected error from SpawnTab, got nil")
			}
			for _, want := range []string{
				"macOS Accessibility permission for Terminal",
				`"Terminal"`,
				"x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
				"NOT Claude Code",
				"NOT the flow binary",
				"flow do",
				tc.stderr, // underlying error preserved via %w
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("wrapped error missing %q\n--- error ---\n%v", want, err)
				}
			}
		})
	}
}

// TestSpawnTabPassesThroughUnknownErrors verifies that non-Accessibility
// errors from osascript are returned unchanged — only the specific
// permission-denied patterns trigger the wrap.
func TestSpawnTabPassesThroughUnknownErrors(t *testing.T) {
	old := Runner
	want := errors.New("osascript failed: exit status 1: some unrelated AppleScript error")
	Runner = func(args []string) error { return want }
	t.Cleanup(func() { Runner = old })

	err := SpawnTab("t", "/tmp", "echo hi", nil)
	if err == nil {
		t.Fatal("expected error from SpawnTab, got nil")
	}
	if err.Error() != want.Error() {
		t.Errorf("expected pass-through of unrelated error, got:\n%v", err)
	}
	if strings.Contains(err.Error(), "Accessibility") {
		t.Errorf("non-Accessibility error should not be wrapped with permission text:\n%v", err)
	}
}

// TestIsAccessibilityDenied unit-tests the pattern matcher in isolation.
func TestIsAccessibilityDenied(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"is not allowed to send keystrokes", true},
		{"not allowed assistive access", true},
		{"not authorized to send Apple events", true},
		{"some error (-1002)", true},
		{"some error (-1743)", true},
		{"some error (-25211)", true},
		{"completely unrelated AppleScript syntax error", false},
		{"", false},
	}
	for _, tc := range cases {
		got := isAccessibilityDenied(errors.New(tc.msg))
		if got != tc.want {
			t.Errorf("isAccessibilityDenied(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
	// Nil error must never be flagged as Accessibility-denied.
	if isAccessibilityDenied(nil) {
		t.Error("isAccessibilityDenied(nil) should be false")
	}
}

// TestShellQuote is a sanity check on the local helper — same contract
// as iterm.ShellQuote.
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
