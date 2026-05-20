package claude

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
		"658bf2be-5ae3-4842-a8a4-e0d0b785514d",
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
		"658BF2BE-5AE3-4842-A8A4-E0D0B785514D", // uppercase: not allowed by claude --session-id
		"658bf2be-5ae3-1842-a8a4-e0d0b785514d", // version digit ≠ 4
		"658bf2be-5ae3-4842-c8a4-e0d0b785514d", // variant nibble outside 8-b
	}
	for _, b := range bad {
		if err := h.ValidateSessionID(b); err == nil {
			t.Errorf("ValidateSessionID(%q) = nil, want error", b)
		}
	}
}

// TestEncodeCwd pins the empirical rule derived from
// ~/.claude/projects/* vs. the original cwd recorded in each dir's
// *.jsonl. `/`, `.`, and `_` each map to `-`; everything else is
// unchanged. If a new sample surfaces that needs a different rule,
// add the observed pair here before touching encodeCwd.
func TestEncodeCwd(t *testing.T) {
	cases := []struct {
		cwd, want string
	}{
		// Plain path — only slashes transform.
		{"/Users/alice/code/myapp", "-Users-alice-code-myapp"},
		// Dotfile segment: `.flow` becomes `-flow`, producing a double
		// dash after `alice`.
		{"/Users/alice/.flow/tasks/add-oauth/workspace",
			"-Users-alice--flow-tasks-add-oauth-workspace"},
		// Underscores in a path segment also transform — observed on
		// Terraform module paths with numeric prefixes.
		{"/Users/alice/monorepo/tf/modules/1_input_instance/application_gcp",
			"-Users-alice-monorepo-tf-modules-1-input-instance-application-gcp"},
		// Underscore-prefix dir — seen in some workspace trees;
		// `/_default` becomes `--default`.
		{"/Users/alice/.workspaces/instances/default/projects/abc/def/_default",
			"-Users-alice--workspaces-instances-default-projects-abc-def--default"},
		// Hyphens, digits, and mixed case pass through unchanged.
		{"/Users/alice/Downloads/my-charts-45dae5e1171f",
			"-Users-alice-Downloads-my-charts-45dae5e1171f"},
	}
	for _, tc := range cases {
		if got := EncodeCwd(tc.cwd); got != tc.want {
			t.Errorf("EncodeCwd(%q) = %q, want %q", tc.cwd, got, tc.want)
		}
		if got := encodeCwd(tc.cwd); got != tc.want {
			t.Errorf("encodeCwd(%q) = %q, want %q", tc.cwd, got, tc.want)
		}
	}
}

// TestLaunchCmd_PreservesByteIdentity verifies the LaunchCmd output
// is byte-identical to the legacy command string that the
// pre-refactor do.go was producing — same flag order, same quoting,
// same trailing --dangerously-skip-permissions placement.
func TestLaunchCmd_PreservesByteIdentity(t *testing.T) {
	h := New()
	sessionID := "658bf2be-5ae3-4842-a8a4-e0d0b785514d"
	prompt := "do the thing"

	// No injection, no skip-approvals.
	got := h.LaunchCmd(sessionID, prompt, harness.LaunchOpts{})
	want := "claude --session-id " + sessionID + " 'do the thing'"
	if got != want {
		t.Errorf("LaunchCmd plain:\n got=%q\nwant=%q", got, want)
	}

	// Skip-approvals appended at the END (matches legacy ordering).
	got = h.LaunchCmd(sessionID, prompt, harness.LaunchOpts{SkipPermissions: true})
	want = "claude --session-id " + sessionID + " 'do the thing' --dangerously-skip-permissions"
	if got != want {
		t.Errorf("LaunchCmd skip:\n got=%q\nwant=%q", got, want)
	}

	// Injection appends to the prompt before quoting.
	got = h.LaunchCmd(sessionID, prompt, harness.LaunchOpts{Inject: "extra instr"})
	if !strings.Contains(got, "\n\n"+harness.InjectionMarker+"\nextra instr") {
		t.Errorf("LaunchCmd inject: missing marker+text in %q", got)
	}
}

func TestResumeCmd_PreservesByteIdentity(t *testing.T) {
	h := New()
	sessionID := "658bf2be-5ae3-4842-a8a4-e0d0b785514d"

	got := h.ResumeCmd(sessionID, harness.LaunchOpts{})
	want := "claude --resume " + sessionID
	if got != want {
		t.Errorf("ResumeCmd plain:\n got=%q\nwant=%q", got, want)
	}

	got = h.ResumeCmd(sessionID, harness.LaunchOpts{SkipPermissions: true})
	want = "claude --resume " + sessionID + " --dangerously-skip-permissions"
	if got != want {
		t.Errorf("ResumeCmd skip:\n got=%q\nwant=%q", got, want)
	}

	got = h.ResumeCmd(sessionID, harness.LaunchOpts{Inject: "follow up"})
	want = "claude --resume " + sessionID + " '" + harness.InjectionMarker + "\nfollow up'"
	if got != want {
		t.Errorf("ResumeCmd inject:\n got=%q\nwant=%q", got, want)
	}
}

// TestLiveSessions_ParsesPSOutput verifies the ps-grep heuristic
// against a representative process list. Lines without "claude" are
// skipped; lines with the binary + --session-id/--resume contribute
// to the count map. Duplicate UUID mentions on the same row count
// once (some shells echo argv). Uppercase UUIDs lowercase on read.
func TestLiveSessions_ParsesPSOutput(t *testing.T) {
	orig := PSRunner
	t.Cleanup(func() { PSRunner = orig })

	sample := `  PID COMMAND
 1001 claude --session-id 658bf2be-5ae3-4842-a8a4-e0d0b785514d
 1002 /Users/x/.bun/bin/claude --resume 658bf2be-5ae3-4842-a8a4-e0d0b785514d --foo
 1003 /usr/bin/grep --session-id 11111111-1111-4111-8111-111111111111
 1004 claude --session-id ABCDEF12-3456-4789-A123-456789012345
 1005 some other process
`
	PSRunner = func() ([]byte, error) { return []byte(sample), nil }
	live, err := New().LiveSessionIDs()
	if err != nil {
		t.Fatalf("LiveSessionIDs: %v", err)
	}
	want := map[string]int{
		"658bf2be-5ae3-4842-a8a4-e0d0b785514d": 2,
		"abcdef12-3456-4789-a123-456789012345": 1,
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

// TestLiveSessions_BareClaude — a bare `claude` invocation (no
// --session-id, no --resume) contributes no UUID. Detection requires
// the flag.
func TestLiveSessions_BareClaude(t *testing.T) {
	orig := PSRunner
	t.Cleanup(func() { PSRunner = orig })
	sample := `  PID COMMAND
 77777 /usr/local/bin/claude
 77778 claude --dangerously-skip-permissions
`
	PSRunner = func() ([]byte, error) { return []byte(sample), nil }
	live, err := New().LiveSessionIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Errorf("bare claude should not contribute; got %v", live)
	}
}

// TestLiveSessions_PSError surfaces the ps failure to the caller.
// Callers in app/ swallow this silently — that's their policy. The
// harness layer just returns the error.
func TestLiveSessions_PSError(t *testing.T) {
	orig := PSRunner
	t.Cleanup(func() { PSRunner = orig })
	PSRunner = func() ([]byte, error) { return nil, errors.New("ps blew up") }
	live, err := New().LiveSessionIDs()
	if err == nil {
		t.Errorf("expected error, got nil (live=%v)", live)
	}
}
