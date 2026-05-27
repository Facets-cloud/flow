package wt

import (
	"strings"
	"testing"
)

// TestSpawnTabBuildsExpectedArgv verifies that wt.exe is invoked with
// the grammar Windows Terminal expects (-w 0 nt -d <cwd> --title <t>
// powershell.exe -NoExit -Command <ps>), and that the PowerShell
// command embeds the user-supplied command verbatim.
func TestSpawnTabBuildsExpectedArgv(t *testing.T) {
	var got []string
	oldRunner := Runner
	Runner = func(args []string) error {
		got = args
		return nil
	}
	t.Cleanup(func() { Runner = oldRunner })

	if err := SpawnTab("MyTab", `C:\work\repo`, "claude --session-id abc 'hi'", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}

	want := []string{
		"-w", "0",
		"nt",
		"-d", `C:\work\repo`,
		"--title", "MyTab",
		"powershell.exe",
		"-NoExit",
		"-Command", "claude --session-id abc 'hi'",
	}
	if len(got) != len(want) {
		t.Fatalf("argv length mismatch: got %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestSpawnTabInjectsEnvVarPrefix verifies that envVars are emitted
// in sorted order as a `$env:NAME='value';` prefix to the PowerShell
// command. Sorting is required so callers can match output
// deterministically in their own tests.
func TestSpawnTabInjectsEnvVarPrefix(t *testing.T) {
	var got []string
	oldRunner := Runner
	Runner = func(args []string) error {
		got = args
		return nil
	}
	t.Cleanup(func() { Runner = oldRunner })

	env := map[string]string{
		"FLOW_ROOT": `C:\Users\alice\.flow`,
		"FOO":       "bar",
	}
	if err := SpawnTab("T", `C:\cwd`, "claude --session-id abc 'hi'", env); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}

	// Last arg is the -Command value.
	cmdStr := got[len(got)-1]

	wantPrefix := `$env:FLOW_ROOT='C:\Users\alice\.flow'; $env:FOO='bar'; `
	if !strings.HasPrefix(cmdStr, wantPrefix) {
		t.Errorf("env prefix mismatch:\n got=%q\nwant prefix=%q", cmdStr, wantPrefix)
	}
	if !strings.HasSuffix(cmdStr, "claude --session-id abc 'hi'") {
		t.Errorf("command suffix missing: got=%q", cmdStr)
	}
}

// TestSpawnTabFlattensNewlines verifies that embedded newlines in the
// command are replaced by spaces (matching internal/zellij behaviour).
// Required because PowerShell's -Command flag expects a single script
// string.
func TestSpawnTabFlattensNewlines(t *testing.T) {
	var got []string
	oldRunner := Runner
	Runner = func(args []string) error {
		got = args
		return nil
	}
	t.Cleanup(func() { Runner = oldRunner })

	if err := SpawnTab("T", `C:\cwd`, "line1\nline2\nline3", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}

	cmdStr := got[len(got)-1]
	if strings.Contains(cmdStr, "\n") {
		t.Errorf("expected newlines flattened to spaces; got %q", cmdStr)
	}
	if cmdStr != "line1 line2 line3" {
		t.Errorf("flattened command = %q, want %q", cmdStr, "line1 line2 line3")
	}
}

// TestShellQuoteEscapesEmbeddedSingleQuote — PowerShell escapes
// embedded `'` as `''` inside a single-quoted string.
func TestShellQuoteEscapesEmbeddedSingleQuote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "''"},
		{"plain", "'plain'"},
		{"with space", "'with space'"},
		{"it's", "'it''s'"},
		{`back\slash`, `'back\slash'`},
	}
	for _, tc := range cases {
		if got := ShellQuote(tc.in); got != tc.want {
			t.Errorf("ShellQuote(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestFocusSessionReturnsFalse — Windows Terminal exposes no API to
// enumerate or focus existing tabs by inner process, so FocusSession
// returns (false, nil) for any input and lets the caller fall
// through to the "session running elsewhere" message.
func TestFocusSessionReturnsFalse(t *testing.T) {
	cases := []struct {
		sessionID, binary string
	}{
		{"", ""},
		{"11111111-2222-4333-8444-555555555555", "claude"},
	}
	for _, tc := range cases {
		ok, err := FocusSession(tc.sessionID, tc.binary)
		if err != nil {
			t.Errorf("FocusSession(%q, %q) returned err=%v; want nil", tc.sessionID, tc.binary, err)
		}
		if ok {
			t.Errorf("FocusSession(%q, %q) returned true; want false", tc.sessionID, tc.binary)
		}
	}
}
