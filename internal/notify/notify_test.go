package notify

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// captured records one Runner invocation.
type captured struct {
	name string
	args []string
}

// stubRunner replaces Runner and returns a pointer to the call log.
func stubRunner(t *testing.T, err error) *[]captured {
	t.Helper()
	var calls []captured
	old := Runner
	Runner = func(name string, args ...string) error {
		calls = append(calls, captured{name: name, args: args})
		return err
	}
	t.Cleanup(func() { Runner = old })
	return &calls
}

// stubLookPath forces the terminal-notifier-present / -absent branch.
func stubLookPath(t *testing.T, present bool) {
	t.Helper()
	old := LookPath
	LookPath = func(file string) (string, error) {
		if file == "terminal-notifier" && present {
			return "/opt/homebrew/bin/terminal-notifier", nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { LookPath = old })
}

// argValue returns the value following flag in args, or "" if absent.
func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// TestNotifyPrefersTerminalNotifier is the clickable path: every field
// maps to its flag, and -execute carries the click command.
func TestNotifyPrefersTerminalNotifier(t *testing.T) {
	stubLookPath(t, true)
	calls := stubRunner(t, nil)

	err := Notify(Request{
		Title:    "flow: flow-notify",
		Subtitle: "Desktop notifications",
		Message:  "Claude needs permission to edit spawner.go",
		Execute:  "flow focus 3123e5ff-01ed-4d8f-b5f2-4b75020d3f0f",
		Group:    "flow-3123e5ff-01ed-4d8f-b5f2-4b75020d3f0f",
		Sound:    "default",
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(*calls))
	}
	c := (*calls)[0]
	if c.name != "terminal-notifier" {
		t.Errorf("ran %q; want terminal-notifier", c.name)
	}
	for flag, want := range map[string]string{
		"-title":    "flow: flow-notify",
		"-subtitle": "Desktop notifications",
		"-message":  "Claude needs permission to edit spawner.go",
		"-execute":  "flow focus 3123e5ff-01ed-4d8f-b5f2-4b75020d3f0f",
		"-group":    "flow-3123e5ff-01ed-4d8f-b5f2-4b75020d3f0f",
		"-sound":    "default",
	} {
		if got := argValue(c.args, flag); got != want {
			t.Errorf("%s = %q; want %q", flag, got, want)
		}
	}
}

// TestNotifyFallsBackToOsascript — with terminal-notifier absent the
// banner still posts, via osascript, minus the click action.
func TestNotifyFallsBackToOsascript(t *testing.T) {
	stubLookPath(t, false)
	calls := stubRunner(t, nil)

	err := Notify(Request{
		Title:   "flow: flow-notify",
		Message: "needs input",
		Execute: "flow focus 3123e5ff-01ed-4d8f-b5f2-4b75020d3f0f",
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(*calls))
	}
	c := (*calls)[0]
	if c.name != "osascript" {
		t.Fatalf("ran %q; want osascript", c.name)
	}
	script := strings.Join(c.args, " ")
	if !strings.Contains(script, "display notification") {
		t.Errorf("script missing `display notification`: %s", script)
	}
	if !strings.Contains(script, "flow: flow-notify") {
		t.Errorf("script missing title: %s", script)
	}
	// osascript cannot run a command on click; the Execute value must
	// not leak into the AppleScript.
	if strings.Contains(script, "flow focus") {
		t.Errorf("Execute must be dropped on the osascript path: %s", script)
	}
}

// TestNotifyEmptyMessageIsNoop — macOS drops a bodyless notification,
// so flow shouldn't spend a subprocess on one.
func TestNotifyEmptyMessageIsNoop(t *testing.T) {
	stubLookPath(t, true)
	calls := stubRunner(t, nil)

	for _, msg := range []string{"", "   ", "\n\t"} {
		if err := Notify(Request{Title: "t", Message: msg}); err != nil {
			t.Fatalf("Notify(%q): %v", msg, err)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("expected no calls for blank messages, got %d", len(*calls))
	}
}

// TestNotifyOmitsEmptyFields — optional flags must not be passed with
// empty values, which terminal-notifier would treat as real content.
func TestNotifyOmitsEmptyFields(t *testing.T) {
	stubLookPath(t, true)
	calls := stubRunner(t, nil)

	if err := Notify(Request{Message: "body only"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	c := (*calls)[0]
	for _, flag := range []string{"-title", "-subtitle", "-group", "-sound", "-execute"} {
		if hasFlag(c.args, flag) {
			t.Errorf("%s must be omitted when empty; got args %v", flag, c.args)
		}
	}
	if got := argValue(c.args, "-message"); got != "body only" {
		t.Errorf("-message = %q", got)
	}
}

// TestNotifyPropagatesRunnerError — delivery failures surface to the
// caller (which is free to ignore them).
func TestNotifyPropagatesRunnerError(t *testing.T) {
	stubLookPath(t, true)
	stubRunner(t, errors.New("boom"))

	if err := Notify(Request{Message: "x"}); err == nil {
		t.Error("expected the runner error to propagate")
	}
}

// TestAvailable reflects terminal-notifier's presence on PATH.
func TestAvailable(t *testing.T) {
	stubLookPath(t, true)
	if !Available() {
		t.Error("Available() = false with terminal-notifier on PATH")
	}
	stubLookPath(t, false)
	if Available() {
		t.Error("Available() = true with terminal-notifier absent")
	}
}

// TestQuoteAppleScript covers the escaping that keeps user-controlled
// notification text from breaking out of an AppleScript string literal.
// Newlines matter most: AppleScript literals cannot contain them and
// have no \n escape, so they must become `& linefeed &` joins.
func TestQuoteAppleScript(t *testing.T) {
	cases := []struct{ in, want string }{
		{`plain`, `"plain"`},
		{`say "hi"`, `"say \"hi\""`},
		{`back\slash`, `"back\\slash"`},
		{"two\nlines", `"two" & linefeed & "lines"`},
		{``, `""`},
	}
	for _, tc := range cases {
		if got := quoteAppleScript(tc.in); got != tc.want {
			t.Errorf("quoteAppleScript(%q) = %s; want %s", tc.in, got, tc.want)
		}
	}
}

// TestNotifyMessageWithShellMetacharacters — terminal-notifier args are
// passed as separate argv entries, never through a shell, so quotes,
// semicolons and backticks in a question must survive verbatim rather
// than being escaped or executed.
func TestNotifyMessageWithShellMetacharacters(t *testing.T) {
	stubLookPath(t, true)
	calls := stubRunner(t, nil)

	nasty := "run `rm -rf /`; echo \"done\" && $(whoami)"
	if err := Notify(Request{Message: nasty}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := argValue((*calls)[0].args, "-message"); got != nasty {
		t.Errorf("-message = %q; want it passed through verbatim as argv", got)
	}
}

// TestNotifyIgnoreDoNotDisturb — a blocked session stalls until noticed,
// so its banner must punch through Focus modes. Informational banners
// (auto-run completion) leave the flag off and can be suppressed.
func TestNotifyIgnoreDoNotDisturb(t *testing.T) {
	stubLookPath(t, true)

	calls := stubRunner(t, nil)
	if err := Notify(Request{Message: "blocked", IgnoreDoNotDisturb: true}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !hasFlag((*calls)[0].args, "-ignoreDnD") {
		t.Errorf("expected -ignoreDnD when IgnoreDoNotDisturb is set; got %v", (*calls)[0].args)
	}

	calls2 := stubRunner(t, nil)
	if err := Notify(Request{Message: "fyi"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if hasFlag((*calls2)[0].args, "-ignoreDnD") {
		t.Errorf("-ignoreDnD must be opt-in; got %v", (*calls2)[0].args)
	}
}
