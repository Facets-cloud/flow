package spawner

import (
	"flow/internal/iterm"
	"flow/internal/notify"
	"flow/internal/terminal"
	"flow/internal/zellij"
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
			t.Setenv("ZELLIJ", "")
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
	t.Setenv("ZELLIJ", "")
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

// TestDetectZellij verifies the ZELLIJ env var beats TERM_PROGRAM.
// zellij sets ZELLIJ in every shell it spawns, so its presence means
// the user is inside a zellij session regardless of which terminal
// hosts it.
func TestDetectZellij(t *testing.T) {
	t.Setenv("ZELLIJ", "0")
	t.Setenv("TERM_PROGRAM", "iTerm.app") // proves ZELLIJ wins
	Override = ""
	if got := Detect(); got != BackendZellij {
		t.Errorf("Detect() with ZELLIJ=0: got %q, want %q", got, BackendZellij)
	}
}

// TestSpawnTabRoutesToITerm asserts the iterm Runner is the one called
// when Detect() resolves to BackendITerm.
func TestSpawnTabRoutesToITerm(t *testing.T) {
	Override = BackendITerm
	t.Cleanup(func() { Override = "" })

	itermCalled, terminalCalled, zellijCalled := stubAllRunners(t)
	if err := SpawnTab("title", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if !*itermCalled {
		t.Error("expected iterm.Runner to be called")
	}
	if *terminalCalled {
		t.Error("did not expect terminal.Runner to be called")
	}
	if *zellijCalled {
		t.Error("did not expect zellij.Runner to be called")
	}
}

// TestSpawnTabRoutesToTerminal asserts the terminal Runner is the one
// called when Detect() resolves to BackendTerminal.
func TestSpawnTabRoutesToTerminal(t *testing.T) {
	Override = BackendTerminal
	t.Cleanup(func() { Override = "" })

	itermCalled, terminalCalled, zellijCalled := stubAllRunners(t)
	if err := SpawnTab("title", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if *itermCalled {
		t.Error("did not expect iterm.Runner to be called")
	}
	if !*terminalCalled {
		t.Error("expected terminal.Runner to be called")
	}
	if *zellijCalled {
		t.Error("did not expect zellij.Runner to be called")
	}
}

// TestSpawnTabRoutesToZellij asserts the zellij Runner is the one
// called when Detect() resolves to BackendZellij.
func TestSpawnTabRoutesToZellij(t *testing.T) {
	Override = BackendZellij
	t.Cleanup(func() { Override = "" })

	itermCalled, terminalCalled, zellijCalled := stubAllRunners(t)
	if err := SpawnTab("title", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if *itermCalled {
		t.Error("did not expect iterm.Runner to be called")
	}
	if *terminalCalled {
		t.Error("did not expect terminal.Runner to be called")
	}
	if !*zellijCalled {
		t.Error("expected zellij.Runner to be called")
	}
}

// TestFocusSessionRoutesToITerm — when Detect() resolves to iTerm,
// FocusSession invokes the iterm backend.
func TestFocusSessionRoutesToITerm(t *testing.T) {
	Override = BackendITerm
	t.Cleanup(func() { Override = "" })

	itermCalled, terminalCalled, zellijCalled := stubAllFocusBackends(t)
	if _, err := FocusSession("11111111-2222-4333-8444-555555555555"); err != nil {
		t.Fatalf("FocusSession: %v", err)
	}
	if !*itermCalled {
		t.Error("expected iterm focus path to be called")
	}
	if *terminalCalled || *zellijCalled {
		t.Error("only iterm focus path should be called")
	}
}

// TestFocusSessionRoutesToTerminal — Override=Terminal hits the
// terminal backend.
func TestFocusSessionRoutesToTerminal(t *testing.T) {
	Override = BackendTerminal
	t.Cleanup(func() { Override = "" })

	itermCalled, terminalCalled, zellijCalled := stubAllFocusBackends(t)
	if _, err := FocusSession("11111111-2222-4333-8444-555555555555"); err != nil {
		t.Fatalf("FocusSession: %v", err)
	}
	if !*terminalCalled {
		t.Error("expected terminal focus path to be called")
	}
	if *itermCalled || *zellijCalled {
		t.Error("only terminal focus path should be called")
	}
}

// TestFocusSessionRoutesToZellij — Override=Zellij hits the zellij
// backend.
func TestFocusSessionRoutesToZellij(t *testing.T) {
	Override = BackendZellij
	t.Cleanup(func() { Override = "" })

	itermCalled, terminalCalled, zellijCalled := stubAllFocusBackends(t)
	if _, err := FocusSession("11111111-2222-4333-8444-555555555555"); err != nil {
		t.Fatalf("FocusSession: %v", err)
	}
	if !*zellijCalled {
		t.Error("expected zellij focus path to be called")
	}
	if *itermCalled || *terminalCalled {
		t.Error("only zellij focus path should be called")
	}
}

// stubAllFocusBackends replaces the per-backend PSRunner / RunnerOutput
// vars with stubs that flip a bool when the backend's FocusSession
// path runs. Restores originals on cleanup.
func stubAllFocusBackends(t *testing.T) (*bool, *bool, *bool) {
	t.Helper()
	var itermCalled, terminalCalled, zellijCalled bool

	oldITermPS := iterm.PSRunner
	iterm.PSRunner = func() ([]byte, error) {
		itermCalled = true
		return []byte(""), nil // empty ps output -> ttyForClaudeSession returns "" -> (false, nil)
	}
	t.Cleanup(func() { iterm.PSRunner = oldITermPS })

	oldTermPS := terminal.PSRunner
	terminal.PSRunner = func() ([]byte, error) {
		terminalCalled = true
		return []byte(""), nil
	}
	t.Cleanup(func() { terminal.PSRunner = oldTermPS })

	oldZellijRO := zellij.RunnerOutput
	zellij.RunnerOutput = func(args []string) ([]byte, error) {
		zellijCalled = true
		return []byte("[]"), nil // empty pane list -> (false, nil)
	}
	t.Cleanup(func() { zellij.RunnerOutput = oldZellijRO })

	return &itermCalled, &terminalCalled, &zellijCalled
}

// TestNotifyFocusedDispatchesForEachBackend confirms spawner.NotifyFocused
// routes to a backend under every Override value and that the call
// reaches internal/notify.Runner exactly once with the message body.
// All three backends delegate to the same notify.MacOS today, so we
// can't distinguish which one specifically ran from outside —
// observable behavior is identical. This test pins the dispatch
// wiring (no panic, no missed case) and the notify hand-off.
func TestNotifyFocusedDispatchesForEachBackend(t *testing.T) {
	for _, b := range []Backend{BackendITerm, BackendTerminal, BackendZellij} {
		t.Run(string(b), func(t *testing.T) {
			t.Setenv("FLOW_NOTIFY", "")
			Override = b
			t.Cleanup(func() { Override = "" })

			calls := 0
			var gotArgs []string
			oldRunner := notify.Runner
			notify.Runner = func(name string, args []string) error {
				calls++
				gotArgs = args
				return nil
			}
			t.Cleanup(func() { notify.Runner = oldRunner })

			if err := NotifyFocused("Switched to demo"); err != nil {
				t.Fatalf("NotifyFocused under %s: %v", b, err)
			}
			if calls != 1 {
				t.Errorf("notify.Runner calls = %d; want 1", calls)
			}
			joined := strings.Join(gotArgs, " ")
			if !strings.Contains(joined, "Switched to demo") {
				t.Errorf("notify args missing message body: %v", gotArgs)
			}
		})
	}
}

// TestNotifyFocusedRespectsFlowNotifyOff — when the user opts out via
// FLOW_NOTIFY=0, no Runner call happens for any backend.
func TestNotifyFocusedRespectsFlowNotifyOff(t *testing.T) {
	for _, b := range []Backend{BackendITerm, BackendTerminal, BackendZellij} {
		t.Run(string(b), func(t *testing.T) {
			t.Setenv("FLOW_NOTIFY", "0")
			Override = b
			t.Cleanup(func() { Override = "" })

			oldRunner := notify.Runner
			notify.Runner = func(name string, args []string) error {
				t.Errorf("Runner should not fire when FLOW_NOTIFY=0")
				return nil
			}
			t.Cleanup(func() { notify.Runner = oldRunner })

			if err := NotifyFocused("anything"); err != nil {
				t.Errorf("NotifyFocused under FLOW_NOTIFY=0 returned %v; want nil", err)
			}
		})
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

// stubAllRunners replaces all three backend Runner vars with no-op
// stubs that flip a per-runner boolean when called. Restores
// originals on test cleanup. Returns pointers so callers can read
// post-call.
func stubAllRunners(t *testing.T) (*bool, *bool, *bool) {
	t.Helper()
	var itermCalled, terminalCalled, zellijCalled bool

	oldITerm := iterm.Runner
	iterm.Runner = func(args []string) error {
		itermCalled = true
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

	oldZellij := zellij.Runner
	zellij.Runner = func(args []string) error {
		zellijCalled = true
		if len(args) >= 1 && args[0] != "action" {
			t.Errorf("zellij argv does not start with 'action': %v", args)
		}
		return nil
	}
	t.Cleanup(func() { zellij.Runner = oldZellij })

	return &itermCalled, &terminalCalled, &zellijCalled
}
