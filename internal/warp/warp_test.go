package warp

import (
	"errors"
	"strings"
	"testing"
)

// warpStubs captures every interaction with the four mockable vars
// (Runner, OpenURL, WriteScript, removeScript) so tests can assert on
// exactly what SpawnTab did. Restore originals on cleanup.
type warpStubs struct {
	writeCalls    []string // bodies passed to WriteScript
	openCalls     []string // URIs passed to OpenURL
	runnerCalls   [][]string
	removeCalls   []string
	scriptPath    string // returned by WriteScript stub
	writeErr      error
	openErr       error
	runnerErr     error
	removeErr     error
	t             *testing.T
}

func newWarpStubs(t *testing.T) *warpStubs {
	t.Helper()
	s := &warpStubs{t: t, scriptPath: "/tmp/flow-warp-deadbeef.sh"}

	oldWrite := WriteScript
	WriteScript = func(body string) (string, error) {
		s.writeCalls = append(s.writeCalls, body)
		if s.writeErr != nil {
			return "", s.writeErr
		}
		return s.scriptPath, nil
	}
	t.Cleanup(func() { WriteScript = oldWrite })

	oldOpen := OpenURL
	OpenURL = func(uri string) error {
		s.openCalls = append(s.openCalls, uri)
		return s.openErr
	}
	t.Cleanup(func() { OpenURL = oldOpen })

	oldRunner := Runner
	Runner = func(args []string) error {
		s.runnerCalls = append(s.runnerCalls, append([]string(nil), args...))
		return s.runnerErr
	}
	t.Cleanup(func() { Runner = oldRunner })

	oldRemove := removeScript
	removeScript = func(path string) error {
		s.removeCalls = append(s.removeCalls, path)
		return s.removeErr
	}
	t.Cleanup(func() { removeScript = oldRemove })

	return s
}

// TestSpawnTabBasicNoEnv — happy path with no env vars. WriteScript is
// called once with the expected body shape, OpenURL with the correct
// URL-encoded warp:// URI, Runner with `-e <applescript>`. removeScript
// is NOT called on the happy path.
func TestSpawnTabBasicNoEnv(t *testing.T) {
	s := newWarpStubs(t)

	if err := SpawnTab("my-task", "/Users/me/repo", "claude --session-id abc", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}

	if len(s.writeCalls) != 1 {
		t.Fatalf("WriteScript calls = %d, want 1", len(s.writeCalls))
	}
	body := s.writeCalls[0]
	if !strings.HasPrefix(body, "#!/bin/bash\n") {
		t.Errorf("script body missing shebang: %q", body)
	}
	if !strings.Contains(body, "cd '/Users/me/repo' || exit 1") {
		t.Errorf("script body missing cd line: %q", body)
	}
	if !strings.Contains(body, "exec claude --session-id abc") {
		t.Errorf("script body missing exec line: %q", body)
	}

	if len(s.openCalls) != 1 {
		t.Fatalf("OpenURL calls = %d, want 1", len(s.openCalls))
	}
	wantURI := "warp://action/new_tab?path=%2FUsers%2Fme%2Frepo"
	if s.openCalls[0] != wantURI {
		t.Errorf("OpenURL URI = %q, want %q", s.openCalls[0], wantURI)
	}

	if len(s.runnerCalls) != 1 {
		t.Fatalf("Runner calls = %d, want 1", len(s.runnerCalls))
	}
	if len(s.runnerCalls[0]) != 2 || s.runnerCalls[0][0] != "-e" {
		t.Errorf("Runner argv = %v, want [-e <script>]", s.runnerCalls[0])
	}

	if len(s.removeCalls) != 0 {
		t.Errorf("removeScript called %d times on happy path, want 0", len(s.removeCalls))
	}
}

// TestSpawnTabEnvVarsSorted — env vars sorted alphabetically and
// shell-quoted into the `exec env …` line. Exact parity with the
// zellij/iterm contract.
func TestSpawnTabEnvVarsSorted(t *testing.T) {
	s := newWarpStubs(t)

	envVars := map[string]string{
		"FLOW_TASK":    "my-task",
		"FLOW_PROJECT": "flow",
		"ZEBRA":        "stripes and spaces",
	}
	if err := SpawnTab("flow/my-task", "/repo", "claude --resume abc", envVars); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}

	body := s.writeCalls[0]
	want := "exec env FLOW_PROJECT='flow' FLOW_TASK='my-task' ZEBRA='stripes and spaces' claude --resume abc\n"
	if !strings.Contains(body, want) {
		t.Errorf("script body missing exec env line.\n got: %q\nwant: %q\nfull body:\n%s", "(see body)", want, body)
	}
}

// TestSpawnTabSetsTitleViaOSC2 — when title is non-empty, the OSC 2
// escape sequence is emitted as a printf line before cd.
func TestSpawnTabSetsTitleViaOSC2(t *testing.T) {
	s := newWarpStubs(t)

	if err := SpawnTab("warp-smoke", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}

	body := s.writeCalls[0]
	want := `printf '\033]2;%s\007' 'warp-smoke'`
	if !strings.Contains(body, want) {
		t.Errorf("script body missing OSC 2 title line.\n got body:\n%s\nwant substring: %q", body, want)
	}
}

// TestSpawnTabNoTitleOmitsOSC2 — empty title skips the OSC 2 line
// entirely (no stray printf in the script).
func TestSpawnTabNoTitleOmitsOSC2(t *testing.T) {
	s := newWarpStubs(t)

	if err := SpawnTab("", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}

	body := s.writeCalls[0]
	if strings.Contains(body, `printf '\033]2;`) {
		t.Errorf("empty title should not emit OSC 2 line, but body has one:\n%s", body)
	}
}

// TestScriptBodySelfDeletes — first non-shebang line is rm -- "$0".
// This is what makes the temp script disappear ~immediately on
// successful execution.
func TestScriptBodySelfDeletes(t *testing.T) {
	s := newWarpStubs(t)

	if err := SpawnTab("t", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}

	body := s.writeCalls[0]
	lines := strings.Split(body, "\n")
	if len(lines) < 2 {
		t.Fatalf("script body too short: %q", body)
	}
	if lines[0] != "#!/bin/bash" {
		t.Errorf("line 0 = %q, want shebang", lines[0])
	}
	if lines[1] != `rm -- "$0"` {
		t.Errorf("line 1 = %q, want self-delete line", lines[1])
	}
}

// TestScriptBodyExecReplaces — the final non-empty line is exec, so
// bash is replaced by the command process. No orphan bash, clean ^C.
func TestScriptBodyExecReplaces(t *testing.T) {
	s := newWarpStubs(t)

	if err := SpawnTab("t", "/tmp", "claude", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}

	body := s.writeCalls[0]
	// Last non-empty line.
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "exec ") {
		t.Errorf("last line = %q, want it to start with `exec `", last)
	}
}

// TestAppleScriptHasWasRunningBranch — the keystroke AppleScript
// contains the warm/cold delay branch and keystrokes `bash <path>`
// + Return. Validates the cold-start fix is in place.
func TestAppleScriptHasWasRunningBranch(t *testing.T) {
	s := newWarpStubs(t)

	if err := SpawnTab("t", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}

	if len(s.runnerCalls) != 1 || len(s.runnerCalls[0]) != 2 {
		t.Fatalf("expected one Runner call with 2 args, got %v", s.runnerCalls)
	}
	script := s.runnerCalls[0][1]
	for _, want := range []string{
		`application id "dev.warp.Warp-Stable" is running`,
		`tell application "Warp" to activate`,
		"if wasRunning then",
		"delay 0.6",
		"delay 1.8",
		"end if",
		`keystroke "bash `,
		"delay 0.5", // settle delay between typed text and CR — load-bearing
		`keystroke (ASCII character 13)`, // PTY-level CR — bypasses Warp's synthetic-Return filter
	} {
		if !strings.Contains(script, want) {
			t.Errorf("AppleScript missing %q. Full script:\n%s", want, script)
		}
	}
	// The AppleScript must reference the exact path WriteScript
	// returned — not some other string accidentally interpolated.
	// Without this assertion, a refactor that mis-wired the path
	// argument to buildAppleScript would pass every other test.
	if !strings.Contains(script, s.scriptPath) {
		t.Errorf("AppleScript should reference scriptPath %q, got:\n%s", s.scriptPath, script)
	}
	// Guard against regressions: Warp v0.2026.04+ blocks "Return key"
	// events (key code 36, keystroke return). The only working
	// submission is ASCII character 13.
	for _, bad := range []string{
		"keystroke return",
		"key code 36",
	} {
		if strings.Contains(script, bad) {
			t.Errorf("AppleScript should NOT use %q (Warp filters Return-key events; use ASCII char 13). Full script:\n%s", bad, script)
		}
	}
}

// TestSpawnTabPropagatesWriteScriptError — when WriteScript fails,
// OpenURL and Runner are never invoked.
func TestSpawnTabPropagatesWriteScriptError(t *testing.T) {
	s := newWarpStubs(t)
	s.writeErr = errors.New("disk full")

	err := SpawnTab("t", "/tmp", "echo hi", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(s.openCalls) != 0 {
		t.Errorf("OpenURL called %d times after write failure, want 0", len(s.openCalls))
	}
	if len(s.runnerCalls) != 0 {
		t.Errorf("Runner called %d times after write failure, want 0", len(s.runnerCalls))
	}
	if len(s.removeCalls) != 0 {
		t.Errorf("removeScript called %d times after write failure, want 0", len(s.removeCalls))
	}
}

// TestSpawnTabPropagatesOpenURLError — when OpenURL fails, Runner is
// never invoked and removeScript cleans up the orphaned temp file.
func TestSpawnTabPropagatesOpenURLError(t *testing.T) {
	s := newWarpStubs(t)
	s.openErr = errors.New("simulated open failure")

	err := SpawnTab("t", "/tmp", "echo hi", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(s.runnerCalls) != 0 {
		t.Errorf("Runner called %d times after open failure, want 0", len(s.runnerCalls))
	}
	if len(s.removeCalls) != 1 || s.removeCalls[0] != s.scriptPath {
		t.Errorf("removeScript calls = %v, want [%q]", s.removeCalls, s.scriptPath)
	}
}

// TestSpawnTabPropagatesRunnerError — when Runner fails,
// removeScript cleans up the orphaned temp file and the error is
// returned unwrapped (for non-Accessibility, non-AppNotFound cases).
func TestSpawnTabPropagatesRunnerError(t *testing.T) {
	s := newWarpStubs(t)
	s.runnerErr = errors.New("simulated osascript failure: ambient")

	err := SpawnTab("t", "/tmp", "echo hi", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(s.removeCalls) != 1 || s.removeCalls[0] != s.scriptPath {
		t.Errorf("removeScript calls = %v, want [%q]", s.removeCalls, s.scriptPath)
	}
}

// TestSpawnTabWrapsAccessibilityError — when Runner returns one of
// the documented Accessibility-denied error patterns, the returned
// error string names "Warp" (not "Terminal") and includes the
// System Settings deeplink.
func TestSpawnTabWrapsAccessibilityError(t *testing.T) {
	cases := []string{
		"osascript failed: exit status 1: System Events got an error: Warp (Warp) is not allowed to send keystrokes. (-1719)",
		"some other framing: not allowed assistive access",
		"not authorized to send Apple events",
		"err (-25211)",
	}
	for _, msg := range cases {
		t.Run(msg, func(t *testing.T) {
			s := newWarpStubs(t)
			s.runnerErr = errors.New(msg)

			err := SpawnTab("t", "/tmp", "echo hi", nil)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			es := err.Error()
			for _, want := range []string{
				"Warp",
				"Accessibility",
				"x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
			} {
				if !strings.Contains(es, want) {
					t.Errorf("wrapped error missing %q:\n%s", want, es)
				}
			}
			if strings.Contains(es, "Terminal.app") || strings.Contains(es, `toggle for "Terminal"`) {
				t.Errorf("wrapped error mentions Terminal (wrong copy):\n%s", es)
			}
		})
	}
}

// TestShellQuote — same contract as iterm.ShellQuote / terminal.ShellQuote
// / zellij.ShellQuote.
func TestShellQuote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain", "'plain'"},
		{"with space", "'with space'"},
		{"with'quote", `'with'\''quote'`},
		{"", "''"},
		{`back\slash`, `'back\slash'`},
	}
	for _, tc := range cases {
		if got := ShellQuote(tc.in); got != tc.want {
			t.Errorf("ShellQuote(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestEscapeAppleScriptString covers the embedded helper directly so
// regressions in the script-path escape don't slip through.
func TestEscapeAppleScriptString(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`plain`, `plain`},
		{`with "quote"`, `with \"quote\"`},
		{`with\backslash`, `with\\backslash`},
		{`/tmp/flow-warp-deadbeef.sh`, `/tmp/flow-warp-deadbeef.sh`},
	}
	for _, tc := range cases {
		if got := escapeAppleScriptString(tc.in); got != tc.want {
			t.Errorf("escapeAppleScriptString(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
