package codex

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"flow/internal/harness"
)

// Codex wake: `codex queue --thread <id> --message <notice>` inserts into
// $CODEX_HOME's queue; the process holding the thread (the interactive
// TUI's app-server daemon) drains it as a new user turn within tens of ms
// when idle, or as its own turn right after the current one when busy
// (task codex-wake-spike). No --resume: that would start a headless turn
// on a closed thread with nobody watching.
//
// Never into an auto run: a live `codex exec resume` consumes the queued
// item, aborts that turn and wedges the thread's queue (spike case 4).

// QueueRunner runs the queue command and returns its stderr. Tests
// replace it.
var QueueRunner = runQueue

func runQueue(argv []string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stderr.String(), err
}

// liveTUI reports whether an interactive Codex TUI has sessionID open and
// no `codex exec` (auto run) holds it.
func liveTUI(sessionID string) bool {
	out, err := PSRunner()
	if err != nil {
		return false
	}
	id := strings.ToLower(sessionID)
	tui := false
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(strings.ToLower(line), "codex") {
			continue
		}
		for _, m := range runningSessionRe.FindAllStringSubmatch(line, -1) {
			if strings.ToLower(m[1]) != id {
				continue
			}
			if isExecLine(line) {
				return false
			}
			tui = true
		}
	}
	return tui
}

func isExecLine(line string) bool {
	for _, f := range strings.Fields(line) {
		if f == "exec" {
			return true
		}
	}
	return false
}

func (c *codex) WakeLive(req harness.WakeRequest) bool { return liveTUI(req.SessionID) }

func (c *codex) Wake(req harness.WakeRequest) (string, error) {
	if !liveTUI(req.SessionID) {
		return "", harness.ErrWakeUnavailable
	}
	stderr, err := QueueRunner([]string{"codex", "queue", "--thread", req.SessionID, "--message", req.Notice}, req.Timeout)
	if err != nil {
		return "", fmt.Errorf("codex queue: %v: %s", err, strings.TrimSpace(stderr))
	}
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Warning:") {
			return strings.TrimSpace(line), nil
		}
	}
	return "", nil
}
