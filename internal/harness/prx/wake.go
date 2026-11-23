package prx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
	cmd, cancel := peerSendCmd(argv, timeout)
	defer cancel()
	err := cmd.Run()
	return cmd.Stderr.(*bytes.Buffer).String(), err
}

// peerSendCmd builds the send, detached from the sender's terminal. An old prx
// without `peer send` reads `peer send …` as a prompt and starts its TUI on
// /dev/tty, which would take over the terminal of whoever ran `flow message`
// (raw mode, left behind after the timeout kill) and leave a junk session. In
// its own session (Setsid) it has no controlling terminal, so /dev/tty can't be
// opened and it fails fast; stdin/stdout stay nil (/dev/null), stderr is captured.
func peerSendCmd(argv []string, timeout time.Duration) (*exec.Cmd, context.CancelFunc) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stderr = &bytes.Buffer{}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Kill the whole session (pgid == pid under Setsid) so a descendant holding
	// stderr can't keep Wait blocked past the timeout; WaitDelay bounds the pipe.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	return cmd, cancel
}

// ChatOnly: a prx push is a peer message, which the receiver frames as chat from
// another session, so it must not carry a user's --with instruction.
func (p *prx) ChatOnly() bool { return true }

// WakeLive reports whether the session has a live owner AND a peer watcher that
// drains its inbox. `prx peer send` appends and exits 0 either way, so without
// the watcher check a session with peers.enabled off (no watcher) would count
// as woken while nothing reads the notice.
func (p *prx) WakeLive(req harness.WakeRequest) bool {
	live, err := p.LiveSessionIDs()
	if err != nil {
		return false
	}
	return live[strings.ToLower(req.SessionID)] > 0 && peerWatching(req.SessionID)
}

// peerRecord is the part of prx's <sessionDir>/.peers/<id>.json flow reads.
// prx's peer watcher publishes it while it runs and withdraws it on shutdown.
type peerRecord struct {
	SessionID string `json:"sessionId"`
	Inbound   string `json:"inbound"`
}

// peerWatching reports whether a prx peer watcher will start a turn on a notice
// sent now: its record exists and its inbound policy is "accept" (or the
// default). "refuse" (app-chat-only sessions never read the inbox) and "hold"
// (messages wait for the human's approval, so no turn starts) both mean no.
// A false "no" is harmless: the mail is stored and the session's hooks deliver.
func peerWatching(sessionID string) bool {
	dir, err := sessionDir()
	if err != nil || sessionID == "" || filepath.Base(sessionID) != sessionID {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".peers", sessionID+".json"))
	if err != nil {
		return false
	}
	var rec peerRecord
	if json.Unmarshal(raw, &rec) != nil || rec.SessionID == "" {
		return false
	}
	if !strings.EqualFold(rec.SessionID, sessionID) {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(rec.Inbound)) {
	case "refuse", "hold":
		return false
	}
	return true
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
