package app

import (
	"errors"
	"testing"
)

// stubPS replaces psRunner with one that returns the given canned output.
func stubPS(t *testing.T, output string) {
	t.Helper()
	old := psRunner
	psRunner = func() ([]byte, error) {
		return []byte(output), nil
	}
	t.Cleanup(func() { psRunner = old })
}

func TestLiveClaudeSessionsExtractsUUIDs(t *testing.T) {
	const psOutput = `  PID COMMAND
12345 /Users/rohit/.bun/bin/claude --session-id 11111111-2222-4333-8444-555555555555
12346 /Users/rohit/.bun/bin/claude --resume aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee
12347 /usr/bin/grep something
12348 claude
`
	stubPS(t, psOutput)
	live, err := liveClaudeSessions()
	if err != nil {
		t.Fatal(err)
	}
	if !live["11111111-2222-4333-8444-555555555555"] {
		t.Errorf("expected --session-id UUID to be detected; got %v", live)
	}
	if !live["aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"] {
		t.Errorf("expected --resume UUID to be detected; got %v", live)
	}
	if len(live) != 2 {
		t.Errorf("got %d live sessions, want 2: %v", len(live), live)
	}
}

func TestLiveClaudeSessionsLowercasesUUIDs(t *testing.T) {
	const psOutput = `  PID COMMAND
99999 /usr/local/bin/claude --session-id AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE
`
	stubPS(t, psOutput)
	live, err := liveClaudeSessions()
	if err != nil {
		t.Fatal(err)
	}
	if !live["aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"] {
		t.Errorf("uppercase UUID not normalized to lowercase: %v", live)
	}
}

func TestLiveClaudeSessionsIgnoresNonClaude(t *testing.T) {
	// A line that contains a UUID but no "claude" token should be ignored.
	const psOutput = `  PID COMMAND
54321 /usr/bin/something --session-id 11111111-2222-4333-8444-555555555555
`
	stubPS(t, psOutput)
	live, err := liveClaudeSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Errorf("non-claude line should not contribute; got %v", live)
	}
}

func TestCountClaudeProcessesForSessionEmptyID(t *testing.T) {
	stubPS(t, "  PID COMMAND\n12345 /bin/claude --session-id 11111111-2222-4333-8444-555555555555\n")
	if got := countClaudeProcessesForSession(""); got != 0 {
		t.Errorf("count for empty UUID = %d, want 0", got)
	}
}

func TestCountClaudeProcessesForSessionSingle(t *testing.T) {
	const psOutput = `  PID COMMAND
12345 /bin/claude --session-id 11111111-2222-4333-8444-555555555555
`
	stubPS(t, psOutput)
	if got := countClaudeProcessesForSession("11111111-2222-4333-8444-555555555555"); got != 1 {
		t.Errorf("count = %d, want 1", got)
	}
}

// TestCountClaudeProcessesForSessionDuplicates pins the duplicate
// detection behavior: two claude processes running the same UUID
// (possible via prior --force) yield a count of 2 so do.go can warn.
func TestCountClaudeProcessesForSessionDuplicates(t *testing.T) {
	const psOutput = `  PID COMMAND
12345 /bin/claude --session-id 11111111-2222-4333-8444-555555555555
12346 /bin/claude --resume 11111111-2222-4333-8444-555555555555
12347 /bin/claude --session-id ffffffff-ffff-4fff-8fff-ffffffffffff
`
	stubPS(t, psOutput)
	if got := countClaudeProcessesForSession("11111111-2222-4333-8444-555555555555"); got != 2 {
		t.Errorf("count for duplicates = %d, want 2", got)
	}
}

func TestCountClaudeProcessesForSessionPSError(t *testing.T) {
	old := psRunner
	psRunner = func() ([]byte, error) {
		return nil, errors.New("ps blew up")
	}
	t.Cleanup(func() { psRunner = old })
	if got := countClaudeProcessesForSession("11111111-2222-4333-8444-555555555555"); got != 0 {
		t.Errorf("count on ps error = %d, want 0 (best-effort)", got)
	}
}

func TestLiveClaudeSessionsIgnoresBareClaude(t *testing.T) {
	// A bare `claude` invocation (no --session-id, no --resume) does not
	// contribute a UUID to the live set, since none is parseable.
	const psOutput = `  PID COMMAND
77777 /usr/local/bin/claude
77778 claude --dangerously-skip-permissions
`
	stubPS(t, psOutput)
	live, err := liveClaudeSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Errorf("bare claude should not contribute; got %v", live)
	}
}
