package spawner

import (
	"flow/internal/iterm"
	"flow/internal/terminal"
	"strings"
	"testing"
)

// TestDetectFromEnv verifies the TERM_PROGRAM → backend mapping. The
// Override knob has higher precedence and is checked separately below.
func TestDetectFromEnv(t *testing.T) {
	cases := []struct {
		termProgram string
		want        Backend
	}{
		{"iTerm.app", BackendITerm},
		{"Apple_Terminal", BackendTerminal},
		{"", BackendITerm},
		{"WezTerm", BackendITerm},
		{"vscode", BackendITerm},
	}
	for _, tc := range cases {
		t.Run(tc.termProgram, func(t *testing.T) {
			t.Setenv("TERM_PROGRAM", tc.termProgram)
			Override = ""
			if got := Detect(); got != tc.want {
				t.Errorf("Detect() with TERM_PROGRAM=%q: got %q, want %q",
					tc.termProgram, got, tc.want)
			}
		})
	}
}

// TestOverrideBeatsEnv confirms the test escape hatch: setting Override
// pins the backend regardless of TERM_PROGRAM, so individual tests can
// pin the dispatcher without relying on env-var mutation order.
func TestOverrideBeatsEnv(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	t.Cleanup(func() { Override = "" })

	Override = BackendTerminal
	if got := Detect(); got != BackendTerminal {
		t.Errorf("Override=Terminal: got %q, want %q", got, BackendTerminal)
	}
	Override = BackendITerm
	if got := Detect(); got != BackendITerm {
		t.Errorf("Override=ITerm: got %q, want %q", got, BackendITerm)
	}
}

// TestSpawnTabRoutesToITerm asserts the iterm Runner is the one called
// when Detect() resolves to BackendITerm.
func TestSpawnTabRoutesToITerm(t *testing.T) {
	Override = BackendITerm
	t.Cleanup(func() { Override = "" })

	itermCalled, terminalCalled := stubBothRunners(t)
	if err := SpawnTab("title", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if !*itermCalled {
		t.Error("expected iterm.Runner to be called")
	}
	if *terminalCalled {
		t.Error("did not expect terminal.Runner to be called")
	}
}

// TestSpawnTabRoutesToTerminal asserts the terminal Runner is the one
// called when Detect() resolves to BackendTerminal.
func TestSpawnTabRoutesToTerminal(t *testing.T) {
	Override = BackendTerminal
	t.Cleanup(func() { Override = "" })

	itermCalled, terminalCalled := stubBothRunners(t)
	if err := SpawnTab("title", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if *itermCalled {
		t.Error("did not expect iterm.Runner to be called")
	}
	if !*terminalCalled {
		t.Error("expected terminal.Runner to be called")
	}
}

// TestShellQuoteParity makes sure the re-exported helper matches
// iterm's implementation. Both backends quote identically.
func TestShellQuoteParity(t *testing.T) {
	cases := []string{"plain", "with space", "with'quote", `back\slash`}
	for _, in := range cases {
		if got, want := ShellQuote(in), iterm.ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q): got %q, want %q", in, got, want)
		}
	}
}

// stubBothRunners replaces both backends' Runner vars with no-op stubs
// that flip a per-runner boolean when called. Restores originals on
// test cleanup. Returns pointers so the caller can read post-call.
func stubBothRunners(t *testing.T) (*bool, *bool) {
	t.Helper()
	var itermCalled, terminalCalled bool

	oldITerm := iterm.Runner
	iterm.Runner = func(args []string) error {
		itermCalled = true
		// Sanity-check: every iterm script targets iTerm2.
		if len(args) >= 2 && !strings.Contains(args[1], "iTerm2") {
			t.Errorf("iterm script does not target iTerm2: %s", args[1])
		}
		return nil
	}
	t.Cleanup(func() { iterm.Runner = oldITerm })

	oldTerm := terminal.Runner
	terminal.Runner = func(args []string) error {
		terminalCalled = true
		if len(args) >= 2 && !strings.Contains(args[1], `"Terminal"`) {
			t.Errorf("terminal script does not target Terminal: %s", args[1])
		}
		return nil
	}
	t.Cleanup(func() { terminal.Runner = oldTerm })

	return &itermCalled, &terminalCalled
}
