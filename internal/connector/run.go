package connector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Verbs of contract v1.
const (
	VerbSend = "send" // notify the human about one message
	VerbTest = "test" // send a sample notification (payload id "test")
)

// Payload is the JSON a connector reads on stdin for `send` and `test`.
type Payload struct {
	ID        string   `json:"id"`
	FromTask  string   `json:"from_task"`
	Body      string   `json:"body"`
	Options   []string `json:"options"`
	Urgent    bool     `json:"urgent"`
	Kind      string   `json:"kind"` // "ask" | "test"
	CreatedAt string   `json:"created_at"`
	ReplyHint string   `json:"reply_hint"`
	// FromPeer names the peer hub the message came from ("" for mail
	// from this hub's own tasks).
	FromPeer string `json:"from_peer"`
	// FromConnector names the connector that sent this notice to the
	// human ("" for mail from a task or peer), so a dialog can label it.
	FromConnector string `json:"from_connector"`
}

// Timeout is how long one connector invocation may run before it (and
// every process it started in its group) is killed.
const Timeout = 30 * time.Second

// Run invokes `<exec> <verb>` in the connector's folder with payload on
// stdin and env as its whole environment. Non-zero exit, a start
// failure, or exceeding timeout is an error (stderr tail included). The
// connector runs in its own process group so a timeout kills everything
// it forked; a connector that wants a long-lived child (a dialog) must
// detach it into its own session.
func Run(m *Manifest, verb string, payload []byte, env []string, timeout time.Duration) error {
	_, err := RunOutput(m, verb, payload, env, timeout)
	return err
}

// PeerPayload is the JSON a role-"peer" connector reads on stdin for
// `send`: one message to carry to peer at endpoint At. Kind is "ask",
// "fyi" or "reply" (ReplyToRef set: the other hub's id this answers).
type PeerPayload struct {
	ID         string   `json:"id"`
	Peer       string   `json:"peer"`
	At         string   `json:"at"`
	Kind       string   `json:"kind"`
	Body       string   `json:"body"`
	Options    []string `json:"options"`
	Urgent     bool     `json:"urgent"`
	ReplyToRef string   `json:"reply_to_ref"`
	CreatedAt  string   `json:"created_at"`
}

// RunOutput is Run that also returns the connector's stdout (at most
// 64 KiB are kept). A peer connector may print {"remote_ref":"…"}.
func RunOutput(m *Manifest, verb string, payload []byte, env []string, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.ExecPath(), verb)
	cmd.Dir = m.Dir
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &tailWriter{buf: &stdout, max: 64 << 10}
	cmd.Stderr = &tailWriter{buf: &stderr, max: 2048}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// A grandchild still holding stderr must not stall Wait forever.
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return stdout.Bytes(), fmt.Errorf("%s %s timed out after %s", m.Name, verb, timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		var ee *exec.ExitError
		if errors.As(err, &ee) && msg != "" {
			return stdout.Bytes(), fmt.Errorf("%s %s: %v: %s", m.Name, verb, err, msg)
		}
		return stdout.Bytes(), fmt.Errorf("%s %s: %v", m.Name, verb, err)
	}
	return stdout.Bytes(), nil
}

// tailWriter keeps only the last max bytes written.
type tailWriter struct {
	buf *bytes.Buffer
	max int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	if over := w.buf.Len() - w.max; over > 0 {
		w.buf.Next(over)
	}
	return len(p), nil
}
