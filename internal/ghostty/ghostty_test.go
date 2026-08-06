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

// ghostttyFocusStubs wires the three mockable vars FocusSession touches
// and records what each was asked to do.
type ghosttyFocusStubs struct {
	psOut       string
	psErr       error
	scriptOut   string
	scriptErr   error
	scripts     []string
	activations int
}

func stubFocus(t *testing.T, s *ghosttyFocusStubs) {
	t.Helper()

	oldPS := PSRunner
	PSRunner = func() ([]byte, error) {
		if s.psErr != nil {
			return nil, s.psErr
		}
		return []byte(s.psOut), nil
	}
	t.Cleanup(func() { PSRunner = oldPS })

	oldRO := RunnerOutput
	RunnerOutput = func(args []string) ([]byte, error) {
		if len(args) >= 2 {
			s.scripts = append(s.scripts, args[1])
		}
		if s.scriptErr != nil {
			return nil, s.scriptErr
		}
		return []byte(s.scriptOut), nil
	}
	t.Cleanup(func() { RunnerOutput = oldRO })

	oldAct := ActivateApp
	ActivateApp = func() error {
		s.activations++
		return nil
	}
	t.Cleanup(func() { ActivateApp = oldAct })
}

const focusUUID = "11111111-2222-4333-8444-555555555555"

// TestFocusSessionMatchesAndFocuses is the happy path on Ghostty >=
// 1.4.0: ps maps the session UUID to a tty and the AppleScript reports
// a match.
func TestFocusSessionMatchesAndFocuses(t *testing.T) {
	s := &ghosttyFocusStubs{
		psOut:     "  501 ttys012 claude --resume " + focusUUID + "\n",
		scriptOut: "ok\n",
	}
	stubFocus(t, s)

	focused, err := FocusSession(focusUUID, "claude")
	if err != nil {
		t.Fatalf("FocusSession: %v", err)
	}
	if !focused {
		t.Fatal("expected a focus")
	}
	if len(s.scripts) != 1 {
		t.Fatalf("expected 1 script, got %d", len(s.scripts))
	}
	// Ghostty's per-tab objects are `terminals` (not iTerm2's
	// `sessions`), and `focus` is a command — `selected`/`index` are
	// read-only, so `set selected` would silently no-op.
	script := s.scripts[0]
	for _, want := range []string{`tell application "Ghostty"`, "terminals of t", "/dev/ttys012", "focus trm"} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "sessions of t") {
		t.Error("script uses iTerm2's `sessions`; Ghostty exposes `terminals`")
	}
	if s.activations != 0 {
		t.Errorf("happy path must not use the degraded activate fallback, got %d", s.activations)
	}
}

// TestFocusSessionOldGhosttyDegrades is the version gate. Ghostty <=
// v1.3.1 has no `tty` property on its terminal class, so the script
// errors. That must degrade to activate-and-miss, NOT surface as a
// backend error.
func TestFocusSessionOldGhosttyDegrades(t *testing.T) {
	s := &ghosttyFocusStubs{
		psOut:     "  501 ttys012 claude --resume " + focusUUID + "\n",
		scriptErr: errNoTTYProperty{},
	}
	stubFocus(t, s)

	focused, err := FocusSession(focusUUID, "claude")
	if err != nil {
		t.Fatalf("old-Ghostty script error must not surface as an error, got %v", err)
	}
	if focused {
		t.Error("must report a miss when the tty property is unavailable")
	}
	if s.activations != 1 {
		t.Errorf("expected the degraded path to activate Ghostty once, got %d", s.activations)
	}
}

type errNoTTYProperty struct{}

func (errNoTTYProperty) Error() string {
	return `osascript: execution error: Ghostty got an error: Can't get tty of terminal 1. (-1728)`
}

// TestFocusSessionEmptyID must not touch ps, osascript, or the app.
func TestFocusSessionEmptyID(t *testing.T) {
	s := &ghosttyFocusStubs{psOut: "should not be read"}
	stubFocus(t, s)

	focused, err := FocusSession("", "claude")
	if err != nil || focused {
		t.Fatalf("empty id: got (%v, %v); want (false, nil)", focused, err)
	}
	if len(s.scripts) != 0 || s.activations != 0 {
		t.Error("empty id must be a pure no-op")
	}
}

// TestFocusSessionNoMatchInPS — the session isn't running under this
// harness, so there's no tty to match and no script should run.
func TestFocusSessionNoMatchInPS(t *testing.T) {
	s := &ghosttyFocusStubs{psOut: "  501 ttys012 claude --resume 99999999-8888-4777-8666-555555555555\n"}
	stubFocus(t, s)

	focused, err := FocusSession(focusUUID, "claude")
	if err != nil || focused {
		t.Fatalf("got (%v, %v); want (false, nil)", focused, err)
	}
	if len(s.scripts) != 0 {
		t.Error("no tty match must short-circuit before osascript")
	}
}

// TestFocusSessionScriptMissReturnsFalse — Ghostty is new enough to
// answer, but no open terminal has that tty.
func TestFocusSessionScriptMissReturnsFalse(t *testing.T) {
	s := &ghosttyFocusStubs{
		psOut:     "  501 ttys012 claude --resume " + focusUUID + "\n",
		scriptOut: "miss\n",
	}
	stubFocus(t, s)

	focused, err := FocusSession(focusUUID, "claude")
	if err != nil || focused {
		t.Fatalf("got (%v, %v); want (false, nil)", focused, err)
	}
	if s.activations != 0 {
		t.Error("a clean miss is not the degraded path; must not activate")
	}
}

// TestFocusSessionPSError — a ps failure is a real backend fault and
// must surface, unlike the old-Ghostty script error.
func TestFocusSessionPSError(t *testing.T) {
	s := &ghosttyFocusStubs{psErr: errNoTTYProperty{}}
	stubFocus(t, s)

	if _, err := FocusSession(focusUUID, "claude"); err == nil {
		t.Fatal("expected ps failure to surface as an error")
	}
}

// TestFocusSessionSkipsNoControllingTTY — a background session shows
// "??" for its tty and cannot be focused.
func TestFocusSessionSkipsNoControllingTTY(t *testing.T) {
	s := &ghosttyFocusStubs{psOut: "  501 ?? claude --resume " + focusUUID + "\n"}
	stubFocus(t, s)

	focused, err := FocusSession(focusUUID, "claude")
	if err != nil || focused {
		t.Fatalf("got (%v, %v); want (false, nil)", focused, err)
	}
	if len(s.scripts) != 0 {
		t.Error("a session with no controlling tty must not reach osascript")
	}
}

// TestTTYForHarnessSessionRealPSFormat feeds a verbatim `ps -axo
// pid,tty,command` row captured from a live `flow do` session. The
// synthetic rows in the other tests are hand-written and could drift
// from what macOS actually emits — notably the two-space gap after the
// tty column and the long argv tail carrying the bootstrap prompt.
func TestTTYForHarnessSessionRealPSFormat(t *testing.T) {
	const realRow = "34348 ttys002  claude --session-id 3123e5ff-01ed-4d8f-b5f2-4b75020d3f0f You are the execution session for flow task flow-notify. --dangerously-skip-permissions\n"

	old := PSRunner
	PSRunner = func() ([]byte, error) { return []byte(realRow), nil }
	t.Cleanup(func() { PSRunner = old })

	tty, err := ttyForHarnessSession("3123e5ff-01ed-4d8f-b5f2-4b75020d3f0f", "claude")
	if err != nil {
		t.Fatalf("ttyForHarnessSession: %v", err)
	}
	if tty != "/dev/ttys002" {
		t.Errorf("got tty %q; want /dev/ttys002", tty)
	}
}
