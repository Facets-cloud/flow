package iterm

import (
	"errors"
	"strings"
	"testing"
)

// TestFocusSessionEmptyID short-circuits on empty UUID without touching ps.
func TestFocusSessionEmptyID(t *testing.T) {
	psCalled := false
	old := PSRunner
	PSRunner = func() ([]byte, error) {
		psCalled = true
		return nil, nil
	}
	t.Cleanup(func() { PSRunner = old })

	focused, err := FocusSession("")
	if focused || err != nil {
		t.Errorf("FocusSession(\"\") = (%v, %v); want (false, nil)", focused, err)
	}
	if psCalled {
		t.Error("PSRunner should not be called for empty session id")
	}
}

// TestFocusSessionMatchesAndFocuses confirms the happy path: ps returns a
// claude row carrying the UUID, FocusSession extracts the tty, drives
// osascript with that tty, and reports focused=true when the script
// returns "ok".
func TestFocusSessionMatchesAndFocuses(t *testing.T) {
	const uuid = "11111111-2222-4333-8444-555555555555"
	const psOutput = `  PID TTY      COMMAND
12345 ttys012  /Users/me/.bun/bin/claude --session-id 11111111-2222-4333-8444-555555555555
12346 ttys013  /usr/bin/grep something
`
	stubPS(t, psOutput, nil)
	var captured []string
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		captured = args
		return []byte("ok\n"), nil
	})

	focused, err := FocusSession(uuid)
	if err != nil {
		t.Fatalf("FocusSession: %v", err)
	}
	if !focused {
		t.Fatal("expected focused=true")
	}
	if len(captured) < 2 {
		t.Fatalf("RunnerOutput called with %d args; want >=2", len(captured))
	}
	script := captured[1]
	if !strings.Contains(script, `tell application "iTerm2"`) {
		t.Errorf("script does not target iTerm2: %s", script)
	}
	if !strings.Contains(script, `if tty of s is "/dev/ttys012"`) {
		t.Errorf("script does not match /dev/ttys012: %s", script)
	}
}

// TestFocusSessionResumeFlag covers the --resume variant alongside
// --session-id, since both forms appear depending on whether the tab
// is in bootstrap or resume mode.
func TestFocusSessionResumeFlag(t *testing.T) {
	const uuid = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	const psOutput = `  PID TTY      COMMAND
12345 ttys005  /Users/me/.bun/bin/claude --resume aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee
`
	stubPS(t, psOutput, nil)
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return []byte("ok"), nil
	})

	focused, err := FocusSession(uuid)
	if err != nil || !focused {
		t.Errorf("FocusSession(--resume row) = (%v, %v); want (true, nil)", focused, err)
	}
}

// TestFocusSessionUUIDCaseInsensitive verifies the UUID match is
// case-insensitive (matches the existing liveClaudeSessions
// normalization behavior).
func TestFocusSessionUUIDCaseInsensitive(t *testing.T) {
	const uuid = "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE"
	const psOutput = `  PID TTY      COMMAND
99999 ttys001  /usr/local/bin/claude --session-id aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee
`
	stubPS(t, psOutput, nil)
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return []byte("ok"), nil
	})
	focused, err := FocusSession(uuid)
	if err != nil || !focused {
		t.Errorf("uppercase UUID should match lowercase ps row; got (%v, %v)", focused, err)
	}
}

// TestFocusSessionNoMatchInPS returns (false, nil) without touching osascript.
func TestFocusSessionNoMatchInPS(t *testing.T) {
	const psOutput = `  PID TTY      COMMAND
12345 ttys012  /Users/me/.bun/bin/claude --session-id ffffffff-ffff-4fff-8fff-ffffffffffff
`
	stubPS(t, psOutput, nil)
	osCalled := false
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		osCalled = true
		return nil, nil
	})

	focused, err := FocusSession("11111111-2222-4333-8444-555555555555")
	if focused || err != nil {
		t.Errorf("got (%v, %v); want (false, nil)", focused, err)
	}
	if osCalled {
		t.Error("osascript should not be called when no ps match")
	}
}

// TestFocusSessionSkipsNoControllingTTY ignores claude rows whose tty
// column is "??" — a backgrounded claude with no controlling terminal
// has nothing for us to focus.
func TestFocusSessionSkipsNoControllingTTY(t *testing.T) {
	const uuid = "11111111-2222-4333-8444-555555555555"
	const psOutput = `  PID TTY      COMMAND
12345 ??       /Users/me/.bun/bin/claude --session-id 11111111-2222-4333-8444-555555555555
`
	stubPS(t, psOutput, nil)
	osCalled := false
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		osCalled = true
		return nil, nil
	})

	focused, err := FocusSession(uuid)
	if focused || err != nil {
		t.Errorf("got (%v, %v); want (false, nil) for ?? tty", focused, err)
	}
	if osCalled {
		t.Error("osascript should not be called when tty is ??")
	}
}

// TestFocusSessionScriptMissReturnsFalse covers the case where ps
// found a match but iTerm2's AppleScript walked all sessions without
// finding the tty (e.g., the claude is in a non-iTerm tty, or the
// session was closed between ps and osascript).
func TestFocusSessionScriptMissReturnsFalse(t *testing.T) {
	const uuid = "11111111-2222-4333-8444-555555555555"
	const psOutput = `  PID TTY      COMMAND
12345 ttys012  /Users/me/.bun/bin/claude --session-id 11111111-2222-4333-8444-555555555555
`
	stubPS(t, psOutput, nil)
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return []byte("miss"), nil
	})

	focused, err := FocusSession(uuid)
	if focused || err != nil {
		t.Errorf("got (%v, %v); want (false, nil) for script miss", focused, err)
	}
}

// TestFocusSessionPSError surfaces the error to the caller instead of
// silently treating it as "no match". The caller can then decide
// whether to fall through or surface to the user.
func TestFocusSessionPSError(t *testing.T) {
	stubPS(t, "", errors.New("ps blew up"))
	focused, err := FocusSession("11111111-2222-4333-8444-555555555555")
	if focused {
		t.Error("focused should be false on ps error")
	}
	if err == nil {
		t.Error("expected ps error to be surfaced")
	}
}

// TestFocusSessionOsascriptError surfaces the error too — distinguishes
// "the focus mechanism broke" from "no match found" so the caller can
// log it.
func TestFocusSessionOsascriptError(t *testing.T) {
	const uuid = "11111111-2222-4333-8444-555555555555"
	const psOutput = `  PID TTY      COMMAND
12345 ttys012  /Users/me/.bun/bin/claude --session-id 11111111-2222-4333-8444-555555555555
`
	stubPS(t, psOutput, nil)
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return nil, errors.New("osascript exit 1")
	})

	focused, err := FocusSession(uuid)
	if focused {
		t.Error("focused should be false on osascript error")
	}
	if err == nil {
		t.Error("expected osascript error to be surfaced")
	}
}

func stubPS(t *testing.T, out string, retErr error) {
	t.Helper()
	old := PSRunner
	PSRunner = func() ([]byte, error) {
		return []byte(out), retErr
	}
	t.Cleanup(func() { PSRunner = old })
}

func stubRunnerOutput(t *testing.T, fn func([]string) ([]byte, error)) {
	t.Helper()
	old := RunnerOutput
	RunnerOutput = fn
	t.Cleanup(func() { RunnerOutput = old })
}
