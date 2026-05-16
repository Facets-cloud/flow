package spawner

import (
	"flow/internal/iterm"
	"flow/internal/kitty"
	"flow/internal/terminal"
	"flow/internal/warp"
	"flow/internal/zellij"
	"strings"
	"testing"
)

// TestDetectFromEnv verifies the TERM_PROGRAM → backend mapping. The
// Override knob and the kitty / ZELLIJ checks have higher precedence
// and are checked separately below.
func TestDetectFromEnv(t *testing.T) {
	cases := []struct {
		termProgram string
		want        Backend
	}{
		{"iTerm.app", BackendITerm},
		{"Apple_Terminal", BackendTerminal},
		{"WarpTerminal", BackendWarp},
		{"", BackendITerm},
		{"WezTerm", BackendITerm},
		{"vscode", BackendITerm},
	}
	for _, tc := range cases {
		t.Run(tc.termProgram, func(t *testing.T) {
			t.Setenv("ZELLIJ", "")
			t.Setenv("KITTY_WINDOW_ID", "")
			t.Setenv("WARP_IS_LOCAL_SHELL_SESSION", "")
			t.Setenv("TERM", "")
			t.Setenv("FLOW_TERM", "")
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
// pins the backend regardless of env vars, so individual tests can pin
// the dispatcher without relying on env-var mutation order.
func TestOverrideBeatsEnv(t *testing.T) {
	t.Setenv("ZELLIJ", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("WARP_IS_LOCAL_SHELL_SESSION", "")
	t.Setenv("TERM", "")
	t.Setenv("FLOW_TERM", "zellij")
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
	Override = BackendKitty
	if got := Detect(); got != BackendKitty {
		t.Errorf("Override=Kitty: got %q, want %q", got, BackendKitty)
	}
	Override = BackendWarp
	if got := Detect(); got != BackendWarp {
		t.Errorf("Override=Warp: got %q, want %q", got, BackendWarp)
	}
}

// TestDetectZellij verifies the ZELLIJ env var beats TERM_PROGRAM, kitty,
// and everything else. zellij sets ZELLIJ in every shell it spawns, so
// its presence means the user is inside a zellij session regardless of
// which terminal hosts it.
func TestDetectZellij(t *testing.T) {
	t.Setenv("ZELLIJ", "0")
	t.Setenv("KITTY_WINDOW_ID", "1") // proves ZELLIJ wins over kitty
	t.Setenv("WARP_IS_LOCAL_SHELL_SESSION", "1")
	t.Setenv("TERM", "xterm-kitty") // ditto
	t.Setenv("FLOW_TERM", "iterm")
	t.Setenv("TERM_PROGRAM", "WarpTerminal")
	Override = ""
	if got := Detect(); got != BackendZellij {
		t.Errorf("Detect() with ZELLIJ=0: got %q, want %q", got, BackendZellij)
	}
}

// TestDetectKitty verifies $KITTY_WINDOW_ID and $TERM=xterm-kitty both
// route to BackendKitty, and that kitty beats TERM_PROGRAM (kitty does
// not set TERM_PROGRAM, so without this check kitty users fall back to
// the iTerm path).
func TestDetectKitty(t *testing.T) {
	cases := []struct {
		name          string
		kittyWindowID string
		term          string
		termProgram   string
	}{
		{"KITTY_WINDOW_ID set", "42", "", ""},
		{"TERM=xterm-kitty", "", "xterm-kitty", ""},
		{"both set", "42", "xterm-kitty", ""},
		{"KITTY_WINDOW_ID set even with TERM_PROGRAM=iTerm.app", "42", "", "iTerm.app"},
		{"TERM=xterm-kitty even with TERM_PROGRAM=iTerm.app", "", "xterm-kitty", "iTerm.app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ZELLIJ", "")
			t.Setenv("KITTY_WINDOW_ID", tc.kittyWindowID)
			t.Setenv("WARP_IS_LOCAL_SHELL_SESSION", "1")
			t.Setenv("TERM", tc.term)
			t.Setenv("FLOW_TERM", "iterm")
			t.Setenv("TERM_PROGRAM", tc.termProgram)
			Override = ""
			if got := Detect(); got != BackendKitty {
				t.Errorf("got %q, want %q", got, BackendKitty)
			}
		})
	}
}

// TestDetectWarp verifies Warp's shell markers route to BackendWarp.
func TestDetectWarp(t *testing.T) {
	cases := []struct {
		name                  string
		warpLocalShellSession string
		termProgram           string
	}{
		{"WARP_IS_LOCAL_SHELL_SESSION set", "1", ""},
		{"TERM_PROGRAM=WarpTerminal", "", "WarpTerminal"},
		{"both set", "1", "WarpTerminal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ZELLIJ", "")
			t.Setenv("KITTY_WINDOW_ID", "")
			t.Setenv("TERM", "")
			t.Setenv("FLOW_TERM", "")
			t.Setenv("WARP_IS_LOCAL_SHELL_SESSION", tc.warpLocalShellSession)
			t.Setenv("TERM_PROGRAM", tc.termProgram)
			Override = ""
			if got := Detect(); got != BackendWarp {
				t.Errorf("got %q, want %q", got, BackendWarp)
			}
		})
	}
}

// TestDetectFlowTermOverride verifies FLOW_TERM with a valid backend
// value wins over Warp and TERM_PROGRAM, but loses to ZELLIJ and kitty.
func TestDetectFlowTermOverride(t *testing.T) {
	cases := []struct {
		flowTerm string
		want     Backend
	}{
		{"iterm", BackendITerm},
		{"terminal", BackendTerminal},
		{"zellij", BackendZellij},
		{"kitty", BackendKitty},
		{"warp", BackendWarp},
	}
	for _, tc := range cases {
		t.Run(tc.flowTerm, func(t *testing.T) {
			t.Setenv("ZELLIJ", "")
			t.Setenv("KITTY_WINDOW_ID", "")
			t.Setenv("TERM", "")
			t.Setenv("FLOW_TERM", tc.flowTerm)
			t.Setenv("WARP_IS_LOCAL_SHELL_SESSION", "1")
			t.Setenv("TERM_PROGRAM", "Apple_Terminal")
			Override = ""
			if got := Detect(); got != tc.want {
				t.Errorf("Detect() with FLOW_TERM=%q: got %q, want %q",
					tc.flowTerm, got, tc.want)
			}
		})
	}
}

func TestDetectFlowTermInvalidFallsThrough(t *testing.T) {
	t.Setenv("ZELLIJ", "")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("TERM", "")
	t.Setenv("FLOW_TERM", "garbage-not-a-backend")
	t.Setenv("WARP_IS_LOCAL_SHELL_SESSION", "")
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	Override = ""
	if got := Detect(); got != BackendITerm {
		t.Errorf("Detect() with garbage FLOW_TERM: got %q, want %q", got, BackendITerm)
	}
}

// TestSpawnTabRoutesToITerm asserts the iterm Runner is the one called
// when Detect() resolves to BackendITerm.
func TestSpawnTabRoutesToITerm(t *testing.T) {
	Override = BackendITerm
	t.Cleanup(func() { Override = "" })

	itermCalled, terminalCalled, zellijCalled, kittyCalled, warpCalled := stubAllRunners(t)
	if err := SpawnTab("title", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if !*itermCalled {
		t.Error("expected iterm.Runner to be called")
	}
	if *terminalCalled || *zellijCalled || *kittyCalled || *warpCalled {
		t.Error("only iterm.Runner should be called")
	}
}

// TestSpawnTabRoutesToTerminal asserts the terminal Runner is the one
// called when Detect() resolves to BackendTerminal.
func TestSpawnTabRoutesToTerminal(t *testing.T) {
	Override = BackendTerminal
	t.Cleanup(func() { Override = "" })

	itermCalled, terminalCalled, zellijCalled, kittyCalled, warpCalled := stubAllRunners(t)
	if err := SpawnTab("title", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if !*terminalCalled {
		t.Error("expected terminal.Runner to be called")
	}
	if *itermCalled || *zellijCalled || *kittyCalled || *warpCalled {
		t.Error("only terminal.Runner should be called")
	}
}

// TestSpawnTabRoutesToZellij asserts the zellij Runner is the one
// called when Detect() resolves to BackendZellij.
func TestSpawnTabRoutesToZellij(t *testing.T) {
	Override = BackendZellij
	t.Cleanup(func() { Override = "" })

	itermCalled, terminalCalled, zellijCalled, kittyCalled, warpCalled := stubAllRunners(t)
	if err := SpawnTab("title", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if !*zellijCalled {
		t.Error("expected zellij.Runner to be called")
	}
	if *itermCalled || *terminalCalled || *kittyCalled || *warpCalled {
		t.Error("only zellij.Runner should be called")
	}
}

// TestSpawnTabRoutesToKitty asserts the kitty Runner+RunnerOutput pair
// is invoked when Detect() resolves to BackendKitty.
func TestSpawnTabRoutesToKitty(t *testing.T) {
	Override = BackendKitty
	t.Cleanup(func() { Override = "" })

	itermCalled, terminalCalled, zellijCalled, kittyCalled, warpCalled := stubAllRunners(t)
	if err := SpawnTab("title", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if !*kittyCalled {
		t.Error("expected kitty backend to be called")
	}
	if *itermCalled || *terminalCalled || *zellijCalled || *warpCalled {
		t.Error("only kitty backend should be called")
	}
}

// TestSpawnTabRoutesToWarp asserts the Warp runner path is invoked when
// Detect() resolves to BackendWarp.
func TestSpawnTabRoutesToWarp(t *testing.T) {
	Override = BackendWarp
	t.Cleanup(func() { Override = "" })

	itermCalled, terminalCalled, zellijCalled, kittyCalled, warpCalled := stubAllRunners(t)
	if err := SpawnTab("title", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if !*warpCalled {
		t.Error("expected warp backend to be called")
	}
	if *itermCalled || *terminalCalled || *zellijCalled || *kittyCalled {
		t.Error("only warp backend should be called")
	}
}

// TestShellQuoteParity makes sure the re-exported helper matches
// iterm's implementation. All backends quote identically.
func TestShellQuoteParity(t *testing.T) {
	cases := []string{"plain", "with space", "with'quote", `back\slash`}
	for _, in := range cases {
		if got, want := ShellQuote(in), iterm.ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q): got %q, want %q", in, got, want)
		}
	}
}

// stubAllRunners replaces all backend Runner vars with no-op stubs that
// flip a per-runner boolean when called. Restores originals on test
// cleanup. Returns pointers so callers can read post-call.
func stubAllRunners(t *testing.T) (*bool, *bool, *bool, *bool, *bool) {
	t.Helper()
	var itermCalled, terminalCalled, zellijCalled, kittyCalled, warpCalled bool

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

	oldKitty := kitty.Runner
	kitty.Runner = func(args []string) error {
		kittyCalled = true
		if len(args) >= 1 && args[0] != "@" {
			t.Errorf("kitty argv does not start with '@': %v", args)
		}
		return nil
	}
	t.Cleanup(func() { kitty.Runner = oldKitty })

	// SpawnTab calls RunnerOutput first (kitty @ launch) then Runner
	// (kitty @ send-text). Stub RunnerOutput to return a fake window
	// id so SpawnTab progresses to the Runner call we're asserting on.
	oldKittyRO := kitty.RunnerOutput
	kitty.RunnerOutput = func(args []string) ([]byte, error) {
		kittyCalled = true
		return []byte("1\n"), nil
	}
	t.Cleanup(func() { kitty.RunnerOutput = oldKittyRO })

	oldWarpWrite := warp.WriteScript
	warp.WriteScript = func(body string) (string, error) {
		warpCalled = true
		if !strings.Contains(body, "exec echo hi") {
			t.Errorf("warp bootstrap script missing command: %s", body)
		}
		return "/tmp/flow-warp-test.sh", nil
	}
	t.Cleanup(func() { warp.WriteScript = oldWarpWrite })

	oldWarpOpen := warp.OpenURL
	warp.OpenURL = func(uri string) error {
		warpCalled = true
		if !strings.HasPrefix(uri, "warp://action/new_tab?path=") {
			t.Errorf("warp URI should open a new tab: %s", uri)
		}
		return nil
	}
	t.Cleanup(func() { warp.OpenURL = oldWarpOpen })

	oldWarpRunner := warp.Runner
	warp.Runner = func(args []string) error {
		warpCalled = true
		if len(args) != 2 || args[0] != "-e" {
			t.Errorf("warp Runner argv = %v, want [-e <script>]", args)
		}
		return nil
	}
	t.Cleanup(func() { warp.Runner = oldWarpRunner })

	return &itermCalled, &terminalCalled, &zellijCalled, &kittyCalled, &warpCalled
}
