package praxis

import (
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"flow/internal/harness"
)

// v7, matching what praxis itself mints (uuid.NewV7 in its session
// store) so flow-created sessions sort chronologically alongside native
// ones in the id-keyed sessions directory.
var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewUUIDFormat(t *testing.T) {
	for i := 0; i < 50; i++ {
		id, err := newUUID()
		if err != nil {
			t.Fatalf("newUUID: %v", err)
		}
		if !uuidRe.MatchString(id) {
			t.Errorf("newUUID returned %q, does not match UUID v7 format", id)
		}
		if err := New().ValidateSessionID(id); err != nil {
			t.Errorf("minted id %q fails our own ValidateSessionID: %v", id, err)
		}
	}
}

// The whole point of v7 over v4 here: ids minted later must sort after
// ids minted earlier, because praxis's sessions directory is keyed by id
// and users browse it in order.
func TestNewUUIDIsTimeOrdered(t *testing.T) {
	var ids []string
	for i := 0; i < 5; i++ {
		id, err := newUUID()
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		time.Sleep(2 * time.Millisecond) // cross a millisecond boundary
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Errorf("id %d (%s) does not sort after id %d (%s)", i, ids[i], i-1, ids[i-1])
		}
	}

	// And the timestamp prefix must actually be the current time, not an
	// arbitrary increasing counter.
	prefix := strings.ReplaceAll(ids[0][:13], "-", "")
	ms, err := strconv.ParseInt(prefix, 16, 64)
	if err != nil {
		t.Fatalf("parse timestamp prefix %q: %v", prefix, err)
	}
	if delta := time.Since(time.UnixMilli(ms)); delta < 0 || delta > time.Minute {
		t.Errorf("timestamp prefix decodes to %v (%v away from now)", time.UnixMilli(ms), delta)
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

	// Headless auto run: --session pinned, an explicit turn budget, and
	// --prompt. Praxis run has no --permission-mode flag and Flow must not
	// pass --experimental; the caller enables it through
	// PRAXIS_EXPERIMENTAL in the shell profile.
	got := h.AutoRunArgv(sessionID, prompt, harness.LaunchOpts{SkipPermissions: true})
	want := []string{
		"praxis", "run",
		"--session", sessionID,
		"--max-turns", "1000",
		"--prompt", prompt,
	}
	if len(got) != len(want) {
		t.Fatalf("AutoRunArgv len=%d %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("AutoRunArgv[%d]=%q, want %q", i, got[i], want[i])
		}
	}

	// Injection is appended to the prompt (the last arg) behind the marker.
	inj := h.AutoRunArgv(sessionID, prompt, harness.LaunchOpts{Inject: "extra instr"})
	if !strings.Contains(inj[len(inj)-1], "\n\n"+harness.InjectionMarker+"\nextra instr") {
		t.Errorf("AutoRunArgv inject: missing marker+text in prompt arg %q", inj[len(inj)-1])
	}
}

// The turn budget must be explicit and comfortably above the 25 that
// both `praxis run`'s flag default and the SDK's <=0 fallback resolve to
// — an autonomous run truncated at 25 turns never reaches `flow done`.
func TestAutoRunArgvSetsGenerousTurnBudget(t *testing.T) {
	argv := New().AutoRunArgv("658bf2be-5ae3-4842-a8a4-e0d0b785514d", "p", harness.LaunchOpts{})

	idx := -1
	for i, a := range argv {
		if a == "--max-turns" {
			idx = i
			break
		}
	}
	if idx < 0 || idx == len(argv)-1 {
		t.Fatalf("AutoRunArgv has no --max-turns value: %v", argv)
	}
	turns, err := strconv.Atoi(argv[idx+1])
	if err != nil {
		t.Fatalf("--max-turns %q is not a number: %v", argv[idx+1], err)
	}
	if turns <= 25 {
		t.Errorf("--max-turns is %d; <=25 is the SDK fallback that truncates autonomous runs", turns)
	}
}

func TestPreflightProbesChatSubcommand(t *testing.T) {
	probed := false
	orig := PreflightRunner
	PreflightRunner = func() error {
		probed = true
		return nil
	}
	t.Cleanup(func() { PreflightRunner = orig })

	err := New().Preflight()
	// `praxis` may not exist on the machine running the tests; the PATH
	// check runs first, so only assert probe wiring when it got that far.
	if err == nil && !probed {
		t.Error("Preflight succeeded without probing for the chat subcommand")
	}
	if err != nil && !strings.Contains(err.Error(), "praxis") {
		t.Errorf("Preflight error %q does not name the praxis CLI", err)
	}
}

func TestPreflightReportsMissingChatSubcommand(t *testing.T) {
	if _, err := exec.LookPath("praxis"); err != nil {
		t.Skip("praxis not on PATH; the subcommand probe is unreachable")
	}
	orig := PreflightRunner
	PreflightRunner = func() error { return errors.New("exit status 1") }
	t.Cleanup(func() { PreflightRunner = orig })

	err := New().Preflight()
	if err == nil {
		t.Fatal("Preflight succeeded despite a failing chat probe")
	}
	if !strings.Contains(err.Error(), "chat") {
		t.Errorf("Preflight error %q does not mention the missing chat subcommand", err)
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
