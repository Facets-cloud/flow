package prx

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"flow/internal/harness"
)

// prx wake: `prx peer send -session <id> -from flow -- <notice>` appends the
// notice to the session's peer inbox. The session's peer watcher drains it and
// starts a turn when the session is idle, the same delivery a session-to-session
// message gets, so no listener is armed on flow's side.
//
// WakeLive is the cheap liveness check flow's caller gates on before it checks
// idleness and claims the nudge; Wake is the push itself. The send must not
// happen in WakeLive: the caller checks idleness and claims between the two, so
// a send there would land before both and could duplicate the notice.

// PeerSendRunner runs `prx peer send` and returns its stderr. Tests replace it.
var PeerSendRunner = runPeerSend

func runPeerSend(argv []string, timeout time.Duration) (string, error) {
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

// WakeLive reports whether prx has a live owner for the session right now.
func (p *prx) WakeLive(req harness.WakeRequest) bool {
	live, err := p.LiveSessionIDs()
	if err != nil {
		return false
	}
	return live[strings.ToLower(req.SessionID)] > 0
}

// Wake delivers req.Notice into the live session. A session that went away
// between WakeLive and here is harness.ErrWakeUnavailable (prx exits 3 for "no
// live session"), which the caller treats as not a failure.
func (p *prx) Wake(req harness.WakeRequest) (string, error) {
	if !p.WakeLive(req) {
		return "", harness.ErrWakeUnavailable
	}
	argv := []string{p.Binary(), "peer", "send", "-session", req.SessionID, "-from", "flow", "--", req.Notice}
	stderr, err := PeerSendRunner(argv, req.Timeout)
	if err != nil {
		if exitCode(err) == 3 {
			return "", harness.ErrWakeUnavailable
		}
		return "", fmt.Errorf("prx peer send: %v: %s", err, strings.TrimSpace(stderr))
	}
	return "", nil
}

// exitCode is the process exit status of err, or -1 when it did not exit.
func exitCode(err error) int {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}
