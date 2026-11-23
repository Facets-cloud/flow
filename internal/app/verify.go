package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A brief may carry a checkable "Done when": a `## Verify` section whose
// fenced code blocks list shell commands, one per line. `flow done` runs
// them in the task's work_dir and records each result as a `verify` event
// (a receipt), so whether the checks passed on the final tree is recorded
// by flow — not reported by the agent that wants to be done.
//
//	## Verify
//	```sh
//	go test ./...
//	go vet ./...
//	```
//
// Only fenced lines count (blank lines and `#` comments skipped): prose in
// the section is for humans and is never executed.

// verifyHeadingRe matches the section heading: `## Verify` or `### Verify`.
var verifyHeadingRe = regexp.MustCompile(`(?i)^#{2,3}\s+verify\s*$`)

// verifyMaxOutput caps the output tail kept per receipt.
const verifyMaxOutput = 2000

// defaultVerifyTimeout bounds one command; FLOW_VERIFY_TIMEOUT overrides
// (a Go duration such as 20m).
const defaultVerifyTimeout = 10 * time.Minute

// VerifyReceipt is the detail of one `verify` event.
type VerifyReceipt struct {
	Command    string `json:"command"`
	ExitCode   int    `json:"exit_code"` // -1: could not run, or timed out
	Passed     bool   `json:"passed"`
	DurationMS int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	GitHead    string `json:"git_head,omitempty"`
	GitDirty   *bool  `json:"git_dirty,omitempty"`
	OutputTail string `json:"output_tail,omitempty"`
}

// parseVerifyCommands returns the commands in brief's Verify section.
func parseVerifyCommands(brief string) []string {
	var cmds []string
	inSection, inFence := false, false
	sc := bufio.NewScanner(strings.NewReader(brief))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			if inSection {
				inFence = !inFence
			}
			continue
		}
		if inFence {
			if inSection && trim != "" && !strings.HasPrefix(trim, "#") {
				cmds = append(cmds, trim)
			}
			continue
		}
		if verifyHeadingRe.MatchString(trim) {
			inSection = true
			continue
		}
		if inSection && strings.HasPrefix(trim, "#") {
			inSection = false // the next heading ends the section
		}
	}
	return cmds
}

// taskVerifyCommands reads the task's brief and returns its Verify
// commands; a missing brief has none.
func taskVerifyCommands(slug string) ([]string, error) {
	root, err := flowRoot()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(root, "tasks", slug, "brief.md"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseVerifyCommands(string(b)), nil
}

// verifyRunner runs one command in dir; a package var so tests can stub
// it. It returns the exit code (-1 when the command could not run or
// timed out), the combined output, and whether it timed out.
var verifyRunner = func(dir, command string, timeout time.Duration) (int, string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return -1, string(out), true
	}
	if err == nil {
		return 0, string(out), false
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), string(out), false
	}
	return -1, string(out) + err.Error(), false
}

// gitState returns dir's HEAD and whether its tree has uncommitted
// changes; ok=false when dir is not a git work tree (or git is absent).
var gitState = func(dir string) (head string, dirty bool, ok bool) {
	h, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", false, false
	}
	st, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return "", false, false
	}
	return strings.TrimSpace(string(h)), len(strings.TrimSpace(string(st))) > 0, true
}

// verifyTimeout reads FLOW_VERIFY_TIMEOUT, falling back to the default.
func verifyTimeout() time.Duration {
	if v := os.Getenv("FLOW_VERIFY_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		fmt.Fprintf(os.Stderr, "warning: ignoring FLOW_VERIFY_TIMEOUT=%q (want a duration like 20m)\n", v)
	}
	return defaultVerifyTimeout
}

// runVerifyChecks runs cmds in dir and returns one receipt each. The git
// state is read once, before the checks: it names the tree they verified.
func runVerifyChecks(dir string, cmds []string) []VerifyReceipt {
	head, dirty, gitOK := gitState(dir)
	timeout := verifyTimeout()
	out := make([]VerifyReceipt, 0, len(cmds))
	for _, c := range cmds {
		start := time.Now()
		code, output, timedOut := verifyRunner(dir, c, timeout)
		r := VerifyReceipt{
			Command: c, ExitCode: code, Passed: code == 0 && !timedOut,
			DurationMS: time.Since(start).Milliseconds(), TimedOut: timedOut,
			OutputTail: tail(output, verifyMaxOutput),
		}
		if gitOK {
			d := dirty
			r.GitHead, r.GitDirty = head, &d
		}
		out = append(out, r)
	}
	return out
}

// tail returns the last n bytes of s, starting at a line boundary when
// one is near.
func tail(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < 200 {
		s = s[i+1:]
	}
	return "…" + s
}
