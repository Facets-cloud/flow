package praxis

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"flow/internal/harness"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewUUIDFormat(t *testing.T) {
	for i := 0; i < 50; i++ {
		id, err := newUUID()
		if err != nil {
			t.Fatalf("newUUID: %v", err)
		}
		if !uuidRe.MatchString(id) {
			t.Errorf("newUUID returned %q, does not match UUID v4 format", id)
		}
	}
}

func TestNewUUIDUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id, err := newUUID()
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("duplicate UUID after %d: %s", i, id)
		}
		seen[id] = true
	}
}

func TestValidateSessionID(t *testing.T) {
	h := New()
	good := []string{
		"658bf2be-5ae3-4842-a8a4-e0d0b785514d", // v4
		"01921c3d-4f1e-7c2b-b3a4-3f8e9d2c1b5a", // v7 (praxis uses these)
		"00000000-0000-4000-8000-000000000000",
	}
	for _, g := range good {
		if err := h.ValidateSessionID(g); err != nil {
			t.Errorf("ValidateSessionID(%q) = %v, want nil", g, err)
		}
	}
	bad := []string{
		"",
		"not-a-uuid",
		"658BF2BE-5AE3-4842-A8A4-E0D0B785514D", // uppercase
		"658bf2be-5ae3-0842-a8a4-e0d0b785514d", // version digit 0 (invalid)
		"658bf2be-5ae3-4842-c8a4-e0d0b785514d", // variant nibble outside 8-b
	}
	for _, b := range bad {
		if err := h.ValidateSessionID(b); err == nil {
			t.Errorf("ValidateSessionID(%q) = nil, want error", b)
		}
	}
}

func TestLaunchCmd(t *testing.T) {
	h := New()
	sessionID := "658bf2be-5ae3-4842-a8a4-e0d0b785514d"
	prompt := "do the thing"

	// No injection, no skip-approvals.
	got := h.LaunchCmd(sessionID, prompt, harness.LaunchOpts{})
	want := "praxis chat --session-id " + sessionID + " --prompt 'do the thing'"
	if got != want {
		t.Errorf("LaunchCmd plain:\n got=%q\nwant=%q", got, want)
	}

	// Skip-approvals → --permission-mode yolo appended at the end.
	got = h.LaunchCmd(sessionID, prompt, harness.LaunchOpts{SkipPermissions: true})
	want = "praxis chat --session-id " + sessionID + " --prompt 'do the thing' --permission-mode yolo"
	if got != want {
		t.Errorf("LaunchCmd skip:\n got=%q\nwant=%q", got, want)
	}

	// Injection appends to the prompt before quoting.
	got = h.LaunchCmd(sessionID, prompt, harness.LaunchOpts{Inject: "extra instr"})
	if !strings.Contains(got, "\n\n"+harness.InjectionMarker+"\nextra instr") {
		t.Errorf("LaunchCmd inject: missing marker+text in %q", got)
	}
}

func TestAutoRunArgv(t *testing.T) {
	h := New()
	sessionID := "658bf2be-5ae3-4842-a8a4-e0d0b785514d"
	prompt := "do the thing"

	// Headless auto run: --session pinned, --prompt. Praxis run has no
	// --permission-mode flag and Flow must not pass --experimental; the
	// caller enables it through PRAXIS_EXPERIMENTAL in the shell profile.
	got := h.AutoRunArgv(sessionID, prompt, harness.LaunchOpts{SkipPermissions: true})
	want := []string{"praxis", "run", "--session", sessionID, "--prompt", prompt}
	if len(got) != len(want) {
		t.Fatalf("AutoRunArgv len=%d %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("AutoRunArgv[%d]=%q, want %q", i, got[i], want[i])
		}
	}

	// Injection is appended to the prompt (argv[5]) behind the marker.
	inj := h.AutoRunArgv(sessionID, prompt, harness.LaunchOpts{Inject: "extra instr"})
	if !strings.Contains(inj[5], "\n\n"+harness.InjectionMarker+"\nextra instr") {
		t.Errorf("AutoRunArgv inject: missing marker+text in prompt arg %q", inj[5])
	}
}

func TestResumeCmd(t *testing.T) {
	h := New()
	sessionID := "658bf2be-5ae3-4842-a8a4-e0d0b785514d"

	got := h.ResumeCmd(sessionID, harness.LaunchOpts{})
	want := "praxis chat --session-id " + sessionID
	if got != want {
		t.Errorf("ResumeCmd plain:\n got=%q\nwant=%q", got, want)
	}

	got = h.ResumeCmd(sessionID, harness.LaunchOpts{SkipPermissions: true})
	want = "praxis chat --session-id " + sessionID + " --permission-mode yolo"
	if got != want {
		t.Errorf("ResumeCmd skip:\n got=%q\nwant=%q", got, want)
	}

	got = h.ResumeCmd(sessionID, harness.LaunchOpts{Inject: "follow up"})
	want = "praxis chat --session-id " + sessionID + " --prompt '" + harness.InjectionMarker + "\nfollow up'"
	if got != want {
		t.Errorf("ResumeCmd inject:\n got=%q\nwant=%q", got, want)
	}
}

// TestLiveSessions_ParsesPSOutput verifies the ps-grep heuristic
// against a representative process list. Lines without "praxis" are
// skipped; lines with the binary + --session-id/--resume/--session
// contribute to the count map.
func TestLiveSessions_ParsesPSOutput(t *testing.T) {
	orig := PSRunner
	t.Cleanup(func() { PSRunner = orig })

	sample := `  PID COMMAND
 1001 praxis chat --experimental --session-id 658bf2be-5ae3-4842-a8a4-e0d0b785514d
 1002 /usr/local/bin/praxis chat --session-id 658bf2be-5ae3-4842-a8a4-e0d0b785514d --foo
 1003 praxis run --experimental --session 01921c3d-4f1e-7c2b-b3a4-3f8e9d2c1b5a
 1004 /usr/bin/grep --session-id 11111111-1111-4111-8111-111111111111
 1005 some other process
`
	PSRunner = func() ([]byte, error) { return []byte(sample), nil }
	live, err := New().LiveSessionIDs()
	if err != nil {
		t.Fatalf("LiveSessionIDs: %v", err)
	}
	want := map[string]int{
		"658bf2be-5ae3-4842-a8a4-e0d0b785514d": 2,
		"01921c3d-4f1e-7c2b-b3a4-3f8e9d2c1b5a": 1,
	}
	if len(live) != len(want) {
		t.Fatalf("len(live)=%d, want %d (got %#v)", len(live), len(want), live)
	}
	for k, v := range want {
		if live[k] != v {
			t.Errorf("live[%q] = %d, want %d", k, live[k], v)
		}
	}
}

func TestLiveSessions_BarePraxis(t *testing.T) {
	orig := PSRunner
	t.Cleanup(func() { PSRunner = orig })
	sample := `  PID COMMAND
 77777 /usr/local/bin/praxis
 77778 praxis chat --experimental
`
	PSRunner = func() ([]byte, error) { return []byte(sample), nil }
	live, err := New().LiveSessionIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Errorf("bare praxis should not contribute; got %v", live)
	}
}

func TestLiveSessions_PSError(t *testing.T) {
	orig := PSRunner
	t.Cleanup(func() { PSRunner = orig })
	PSRunner = func() ([]byte, error) { return nil, errors.New("ps blew up") }
	live, err := New().LiveSessionIDs()
	if err == nil {
		t.Errorf("expected error, got nil (live=%v)", live)
	}
}

func TestIdentity(t *testing.T) {
	h := New()
	if h.Name() != harness.Name("praxis") {
		t.Errorf("Name() = %q, want praxis", h.Name())
	}
	if h.Binary() != "praxis" {
		t.Errorf("Binary() = %q, want praxis", h.Binary())
	}
	if h.SessionIDEnvVar() != "PRAXIS_SESSION_ID" {
		t.Errorf("SessionIDEnvVar() = %q, want PRAXIS_SESSION_ID", h.SessionIDEnvVar())
	}
}
