package harness

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// stderrTailBytes caps how much of a failed headless run's stderr rides
// back in the error. Enough for a harness's own diagnosis line plus a
// little context; small enough to stay a warning rather than a wall.
const stderrTailBytes = 800

// RunCapturingStderr runs cmd and, on failure, returns an error carrying
// the tail of what the process wrote to stderr.
//
// The alternative — discarding stderr because the prompt asked the
// harness to work silently — makes every failure indistinguishable:
// `flow done` could only ever report "exit status 1", which is what let
// a mis-declared turn cap in a harness manifest eat close-out sweeps for
// ten days. Harnesses put their reason on stderr ("native: agent: max
// turns reached", "command not found", an auth prompt); that reason is
// the whole diagnosis, so it must survive the exit code.
//
// Stdout is still discarded: on these paths it is the agent's chat
// output, which the prompt already asks to be empty.
//
// cmd.Stderr must not be set by the caller; this function owns it.
func RunCapturingStderr(cmd *exec.Cmd) error {
	var errBuf bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if tail := tailLines(errBuf.String(), stderrTailBytes); tail != "" {
		return fmt.Errorf("%w: %s", err, tail)
	}
	return err
}

// tailLines trims s to its last max bytes on a line boundary, collapsing
// blank lines so a chatty harness doesn't turn one warning into a page.
// An over-long tail is marked with a leading ellipsis so the reader knows
// the start was dropped.
func tailLines(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	truncated := false
	if len(s) > max {
		s = s[len(s)-max:]
		// Drop the partial first line so the tail starts mid-nothing.
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		truncated = true
	}
	lines := make([]string, 0, 8)
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	out := strings.Join(lines, "; ")
	if truncated {
		out = "…" + out
	}
	return out
}
