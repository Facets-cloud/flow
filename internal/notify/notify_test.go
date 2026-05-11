package notify

import (
	"errors"
	"strings"
	"testing"
)

// TestMacOSInvokesOsascript pins the only path: osascript -e with a
// `display notification` script carrying title and body. Both fields
// pass through AppleScript escaping so injection-style content can't
// break the script.
func TestMacOSInvokesOsascript(t *testing.T) {
	var gotName string
	var gotArgs []string
	stubRunner(t, func(name string, args []string) error {
		gotName = name
		gotArgs = args
		return nil
	})

	if err := MacOS("flow", `say "hi"`); err != nil {
		t.Fatalf("MacOS: %v", err)
	}
	if gotName != "osascript" {
		t.Errorf("Runner name = %q; want osascript", gotName)
	}
	if len(gotArgs) < 2 || gotArgs[0] != "-e" {
		t.Fatalf("expected -e script form, got %v", gotArgs)
	}
	script := gotArgs[1]
	if !strings.Contains(script, `display notification`) {
		t.Errorf("script missing 'display notification': %s", script)
	}
	if !strings.Contains(script, `with title "flow"`) {
		t.Errorf("script missing title clause: %s", script)
	}
	if !strings.Contains(script, `\"hi\"`) {
		t.Errorf("body quotes not escaped: %s", script)
	}
}

// TestMacOSSurfacesRunnerError — Runner errors propagate. Callers
// treat them as advisory but the package itself doesn't swallow.
func TestMacOSSurfacesRunnerError(t *testing.T) {
	stubRunner(t, func(name string, args []string) error {
		return errors.New("osascript exit 1")
	})
	if err := MacOS("flow", "x"); err == nil {
		t.Error("expected runner error to propagate")
	}
}

// TestMacOSRespectsFlowNotifyOffSwitch — when FLOW_NOTIFY is set to a
// falsy value, MacOS short-circuits without invoking Runner at all.
func TestMacOSRespectsFlowNotifyOffSwitch(t *testing.T) {
	offValues := []string{"0", "false", "off", "no", "FALSE", "Off", "  no  "}
	for _, v := range offValues {
		t.Run("off/"+v, func(t *testing.T) {
			t.Setenv("FLOW_NOTIFY", v)
			runnerCalled := false
			stubRunner(t, func(string, []string) error {
				runnerCalled = true
				return nil
			})
			if err := MacOS("flow", "hi"); err != nil {
				t.Fatalf("MacOS under FLOW_NOTIFY=%q: %v", v, err)
			}
			if runnerCalled {
				t.Errorf("Runner should not fire when FLOW_NOTIFY=%q", v)
			}
		})
	}

	onValues := []string{"", "1", "true", "yes", "on", "anything-else"}
	for _, v := range onValues {
		t.Run("on/"+v, func(t *testing.T) {
			t.Setenv("FLOW_NOTIFY", v)
			runnerCalled := false
			stubRunner(t, func(string, []string) error {
				runnerCalled = true
				return nil
			})
			if err := MacOS("flow", "hi"); err != nil {
				t.Fatalf("MacOS under FLOW_NOTIFY=%q: %v", v, err)
			}
			if !runnerCalled {
				t.Errorf("Runner should fire when FLOW_NOTIFY=%q", v)
			}
		})
	}
}

// TestEnabledMirrorsMacOSGate — Enabled is exposed so callers that
// want to skip expensive work upstream can ask cheaply. Keep its
// semantics aligned with the gate inside MacOS.
func TestEnabledMirrorsMacOSGate(t *testing.T) {
	t.Setenv("FLOW_NOTIFY", "")
	if !Enabled() {
		t.Error("Enabled() should be true when FLOW_NOTIFY is unset/empty")
	}
	t.Setenv("FLOW_NOTIFY", "0")
	if Enabled() {
		t.Error("Enabled() should be false when FLOW_NOTIFY=0")
	}
	t.Setenv("FLOW_NOTIFY", "off")
	if Enabled() {
		t.Error("Enabled() should be false when FLOW_NOTIFY=off")
	}
	t.Setenv("FLOW_NOTIFY", "1")
	if !Enabled() {
		t.Error("Enabled() should be true when FLOW_NOTIFY=1")
	}
}

// TestEscapeAppleScriptString covers backslash and double-quote
// escaping so injection-style titles can't break the script.
func TestEscapeAppleScriptString(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`hello`, `hello`},
		{`a"b`, `a\"b`},
		{`a\b`, `a\\b`},
		{`"\\`, `\"\\\\`},
	}
	for _, c := range cases {
		if got := escapeAppleScriptString(c.in); got != c.want {
			t.Errorf("escape(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func stubRunner(t *testing.T, fn func(string, []string) error) {
	t.Helper()
	old := Runner
	Runner = fn
	t.Cleanup(func() { Runner = old })
}
