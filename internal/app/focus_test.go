package app

import (
	"flow/internal/harness/claude"
	"flow/internal/iterm"
	"flow/internal/spawner"
	"path/filepath"
	"testing"
)

// TestLooksLikeUUID pins the discriminator that decides whether a
// `flow focus` argument is treated as a session id or a task slug.
func TestLooksLikeUUID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"3123e5ff-01ed-4d8f-b5f2-4b75020d3f0f", true},
		{"3123E5FF-01ED-4D8F-B5F2-4B75020D3F0F", true}, // case-insensitive
		{"11111111-2222-3333-4444-555555555555", true}, // non-v4 still accepted
		{"flow-notify", false},
		{"", false},
		{"3123e5ff-01ed-4d8f-b5f2", false},                    // too few groups
		{"3123e5ff-01ed-4d8f-b5f2-4b75020d3f0f-extra", false}, // too many groups
		{"3123e5ff-01ed-4d8f-b5f2-4b75020d3f0", false},        // last group short
		{"zzzzzzzz-01ed-4d8f-b5f2-4b75020d3f0f", false},       // non-hex
		{"a-really-long-slug-with-five-dashes-x", false},      // 5 groups, wrong widths
	}
	for _, tc := range cases {
		if got := looksLikeUUID(tc.in); got != tc.want {
			t.Errorf("looksLikeUUID(%q) = %v; want %v", tc.in, got, tc.want)
		}
	}
}

// TestFocusUsageErrors covers the argument-count and empty-argument
// paths, which must exit 2 (usage) rather than 1 (runtime).
func TestFocusUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no args", nil},
		{"two refs", []string{"a", "b"}},
		{"blank ref", []string{"   "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rc := cmdFocus(tc.args); rc != 2 {
				t.Errorf("cmdFocus(%v) rc=%d, want 2", tc.args, rc)
			}
		})
	}
}

// TestFocusE2E drives `flow focus` against a real temp flow root: an
// unopened task has no session and must fail cleanly, a task opened via
// `flow do` resolves its session id from its slug, and a bare session id
// works without any task backing it.
func TestFocusE2E(t *testing.T) {
	tmp := t.TempDir()
	flowRoot := filepath.Join(tmp, "flow")
	t.Setenv("FLOW_ROOT", flowRoot)
	t.Setenv("HOME", tmp)

	oldOverride := spawner.Override
	spawner.Override = spawner.BackendITerm
	t.Cleanup(func() { spawner.Override = oldOverride })

	oldOsa := iterm.Runner
	iterm.Runner = func(args []string) error { return nil }
	t.Cleanup(func() { iterm.Runner = oldOsa })

	oldSkip := claude.SkipPermissionsRunner
	claude.SkipPermissionsRunner = func(prompt string) error { return nil }
	t.Cleanup(func() { claude.SkipPermissionsRunner = oldSkip })

	const sessionUUID = "3123e5ff-01ed-4d8f-b5f2-4b75020d3f0f"
	oldNewUUID := claude.NewUUID
	claude.NewUUID = func() (string, error) { return sessionUUID, nil }
	t.Cleanup(func() { claude.NewUUID = oldNewUUID })

	if rc := cmdInit(nil); rc != 0 {
		t.Fatalf("init rc=%d", rc)
	}

	// The focus backends are driven through iterm here (Override above);
	// stub ps so nothing depends on the host's real process table.
	var psOut string
	oldPS := iterm.PSRunner
	iterm.PSRunner = func() ([]byte, error) { return []byte(psOut), nil }
	t.Cleanup(func() { iterm.PSRunner = oldPS })

	var focusScripts []string
	oldRO := iterm.RunnerOutput
	iterm.RunnerOutput = func(args []string) ([]byte, error) {
		if len(args) >= 2 {
			focusScripts = append(focusScripts, args[1])
		}
		return []byte("ok"), nil
	}
	t.Cleanup(func() { iterm.RunnerOutput = oldRO })

	// An unknown slug is a runtime error, not a usage error.
	if rc := cmdFocus([]string{"nope-not-a-task"}); rc != 1 {
		t.Errorf("unknown slug rc=%d, want 1", rc)
	}

	if rc := cmdAdd([]string{"task", "Focus target", "--slug", "focus-target", "--work-dir", tmp}); rc != 0 {
		t.Fatalf("add task rc=%d", rc)
	}

	// A task that has never been opened carries no session id, so there
	// is nothing to focus — exit 1 with the "no session" message.
	if rc := cmdFocus([]string{"focus-target"}); rc != 1 {
		t.Errorf("unopened task rc=%d, want 1", rc)
	}

	// Open it so the task gets a session id bound.
	if rc := cmdDo([]string{"focus-target"}); rc != 0 {
		t.Fatalf("do rc=%d", rc)
	}

	// Now the slug resolves to the session id. ps reports that session
	// on ttys002, so the focus should succeed.
	psOut = "34348 ttys002  claude --session-id " + sessionUUID + " prompt text\n"
	if rc := cmdFocus([]string{"focus-target"}); rc != 0 {
		t.Errorf("focus by slug rc=%d, want 0", rc)
	}
	if len(focusScripts) == 0 {
		t.Fatal("expected an osascript focus attempt")
	}

	// A bare session id works the same way, with no slug lookup.
	focusScripts = nil
	if rc := cmdFocus([]string{sessionUUID}); rc != 0 {
		t.Errorf("focus by session id rc=%d, want 0", rc)
	}
	if len(focusScripts) == 0 {
		t.Error("expected an osascript focus attempt for the bare session id")
	}

	// When ps has no matching row there is no tab to focus: exit 1.
	psOut = ""
	if rc := cmdFocus([]string{"focus-target"}); rc != 1 {
		t.Errorf("focus with no live session rc=%d, want 1", rc)
	}
}
