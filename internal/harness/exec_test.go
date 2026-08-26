package harness

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestRunCapturingStderrSuccessIsQuiet(t *testing.T) {
	// Chat output on stdout is expected and uninteresting; a zero exit
	// is a zero exit.
	if err := RunCapturingStderr(exec.Command("sh", "-c", "echo chatter; echo note >&2")); err != nil {
		t.Fatalf("RunCapturingStderr on a successful command: %v", err)
	}
}

// TestRunCapturingStderrCarriesTheReason is the regression guard for the
// bug this function exists for: `flow done`'s close-out sweep failed 28
// times against the praxis harness and reported nothing but "exit status
// 1", because stderr was discarded. The harness's own diagnosis must ride
// back with the error.
func TestRunCapturingStderrCarriesTheReason(t *testing.T) {
	err := RunCapturingStderr(exec.Command("sh", "-c",
		"echo 'native: agent: max turns reached' >&2; exit 1"))
	if err == nil {
		t.Fatal("want an error from a non-zero exit")
	}
	if !strings.Contains(err.Error(), "native: agent: max turns reached") {
		t.Errorf("error dropped the harness's reason: %v", err)
	}
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Errorf("error dropped the exit status: %v", err)
	}
	// Wrapping, not replacing: callers that inspect the exit code still can.
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error no longer unwraps to *exec.ExitError: %v", err)
	}
	if got := exitErr.ExitCode(); got != 1 {
		t.Errorf("exit code = %d, want 1", got)
	}
}

func TestRunCapturingStderrSilentFailureStaysBare(t *testing.T) {
	err := RunCapturingStderr(exec.Command("sh", "-c", "exit 3"))
	if err == nil {
		t.Fatal("want an error from a non-zero exit")
	}
	// No stderr to report → no trailing punctuation dangling off the message.
	if got := err.Error(); got != "exit status 3" {
		t.Errorf("error = %q, want bare %q", got, "exit status 3")
	}
}

func TestRunCapturingStderrTailIsBounded(t *testing.T) {
	// A harness that streams progress to stderr must not turn one warning
	// into a page — but the last thing it said (the actual reason) has to
	// survive.
	err := RunCapturingStderr(exec.Command("sh", "-c",
		"i=0; while [ $i -lt 200 ]; do echo 'noise noise noise noise noise' >&2; i=$((i+1)); done;"+
			" echo 'the real reason' >&2; exit 1"))
	if err == nil {
		t.Fatal("want an error from a non-zero exit")
	}
	msg := err.Error()
	if !strings.Contains(msg, "the real reason") {
		t.Errorf("bounded tail dropped the last line: %q", msg)
	}
	if len(msg) > stderrTailBytes+64 {
		t.Errorf("error is %d bytes, want <= %d", len(msg), stderrTailBytes+64)
	}
	if !strings.Contains(msg, "…") {
		t.Errorf("truncated tail is not marked as truncated: %q", msg)
	}
}

func TestTailLinesCollapsesBlanks(t *testing.T) {
	got := tailLines("\n\nfirst\n\n   \nsecond\n\n", 800)
	if want := "first; second"; got != want {
		t.Errorf("tailLines = %q, want %q", got, want)
	}
	if got := tailLines("   \n\n ", 800); got != "" {
		t.Errorf("all-whitespace stderr = %q, want empty", got)
	}
}
