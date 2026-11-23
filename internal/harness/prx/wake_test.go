package prx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"flow/internal/harness"
)

// withOwner writes a prx owner record for sid into a temp session dir whose
// pid liveness is pidAlive, and points prx at that dir.
func withOwner(t *testing.T, sid string, pid int, alive bool) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PRAXIS_CODING_AGENT_SESSION_DIR", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".owner"), 0o700); err != nil {
		t.Fatal(err)
	}
	rec := `{"sessionId":"` + sid + `","pid":` + strconv.Itoa(pid) + `}`
	if err := os.WriteFile(filepath.Join(dir, ".owner", sid+".json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := PIDAlive
	PIDAlive = func(p int) bool { return alive && p == pid }
	t.Cleanup(func() { PIDAlive = orig })
	writePeerRecord(t, sid, "")
}

// writePeerRecord publishes the record prx's peer watcher writes while it runs
// (<sessionDir>/.peers/<id>.json); inbound "" is the default policy.
func writePeerRecord(t *testing.T, sid, inbound string) {
	t.Helper()
	dir := os.Getenv("PRAXIS_CODING_AGENT_SESSION_DIR")
	if err := os.MkdirAll(filepath.Join(dir, ".peers"), 0o700); err != nil {
		t.Fatal(err)
	}
	rec := `{"sessionId":"` + sid + `","name":"flow-1","inbound":"` + inbound + `"}`
	if err := os.WriteFile(filepath.Join(dir, ".peers", sid+".json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
}

func removePeerRecord(t *testing.T, sid string) {
	t.Helper()
	if err := os.Remove(filepath.Join(os.Getenv("PRAXIS_CODING_AGENT_SESSION_DIR"), ".peers", sid+".json")); err != nil {
		t.Fatal(err)
	}
}

// fakeSend replaces PeerSendRunner, recording argv and returning err.
func fakeSend(t *testing.T, err error) *[][]string {
	t.Helper()
	var calls [][]string
	orig := PeerSendRunner
	PeerSendRunner = func(argv []string, _ time.Duration) (string, error) {
		calls = append(calls, append([]string(nil), argv...))
		return "", err
	}
	t.Cleanup(func() { PeerSendRunner = orig })
	return &calls
}

func TestWakeLiveFollowsOwnerLiveness(t *testing.T) {
	withOwner(t, "sid-live", 4242, true)
	p := &prx{}
	if !p.WakeLive(harness.WakeRequest{SessionID: "sid-live"}) {
		t.Fatal("live owner: WakeLive = false")
	}
	if p.WakeLive(harness.WakeRequest{SessionID: "sid-other"}) {
		t.Fatal("unknown session: WakeLive = true")
	}
}

func TestWakeLiveFalseWhenOwnerDead(t *testing.T) {
	withOwner(t, "sid-dead", 4343, false)
	if (&prx{}).WakeLive(harness.WakeRequest{SessionID: "sid-dead"}) {
		t.Fatal("dead owner: WakeLive = true")
	}
}

func TestWakeSendsNoticeAsPeerMessage(t *testing.T) {
	withOwner(t, "sid-live", 4242, true)
	calls := fakeSend(t, nil)
	_, err := (&prx{}).Wake(harness.WakeRequest{SessionID: "sid-live", Notice: "1 new flow message", Timeout: time.Second})
	if err != nil {
		t.Fatalf("Wake: %v", err)
	}
	want := []string{"prx", "peer", "send", "-session", "sid-live", "-from", "flow", "--", "1 new flow message"}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0], want) {
		t.Fatalf("argv = %v, want %v", *calls, want)
	}
}

func TestWakeUnavailableWhenNoOwner(t *testing.T) {
	withOwner(t, "sid-live", 4242, true)
	calls := fakeSend(t, nil)
	_, err := (&prx{}).Wake(harness.WakeRequest{SessionID: "gone", Notice: "x"})
	if !errors.Is(err, harness.ErrWakeUnavailable) {
		t.Fatalf("err = %v, want ErrWakeUnavailable", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("send ran for a session with no live owner: %v", *calls)
	}
}

func TestWakeExitThreeIsUnavailable(t *testing.T) {
	withOwner(t, "sid-live", 4242, true)
	exit3 := exec.Command("sh", "-c", "exit 3").Run()
	fakeSend(t, exit3)
	_, err := (&prx{}).Wake(harness.WakeRequest{SessionID: "sid-live", Notice: "x"})
	if !errors.Is(err, harness.ErrWakeUnavailable) {
		t.Fatalf("exit 3: err = %v, want ErrWakeUnavailable", err)
	}
}

func TestWakeOtherExitIsError(t *testing.T) {
	withOwner(t, "sid-live", 4242, true)
	exit4 := exec.Command("sh", "-c", "exit 4").Run()
	fakeSend(t, exit4)
	_, err := (&prx{}).Wake(harness.WakeRequest{SessionID: "sid-live", Notice: "x"})
	if err == nil || errors.Is(err, harness.ErrWakeUnavailable) {
		t.Fatalf("exit 4: err = %v, want a non-unavailable error", err)
	}
}

// B1: an old prx without `peer send` treats `peer` as a prompt and starts its
// TUI on /dev/tty, taking over the sender's terminal. The send must run in its
// own session (no controlling terminal, so /dev/tty can't be opened) with no
// inherited stdio.
func TestPeerSendCmdDetachedFromTTY(t *testing.T) {
	cmd, cancel := peerSendCmd([]string{"prx", "peer", "send"}, time.Second)
	defer cancel()
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatalf("SysProcAttr = %+v, want Setsid", cmd.SysProcAttr)
	}
	if cmd.Stdin != nil || cmd.Stdout != nil {
		t.Fatalf("stdin/stdout must be /dev/null (nil), got %v / %v", cmd.Stdin, cmd.Stdout)
	}
	if cmd.Stderr == nil || cmd.Stderr == os.Stderr {
		t.Fatalf("stderr must be captured, not inherited: %v", cmd.Stderr)
	}
}

// fakePrx puts an executable `prx` script first on PATH.
func fakePrx(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prx"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The real runner: an old prx (exits 1 on `peer`) fails fast and reports a
// plain error, which flow's caller maps to "stored: push failed" (rc 0). The
// script exits 8 unless it leads its own session/process group (Setsid) and 9 if it could
// open a controlling terminal, the two ways it would reach the sender's TTY.
func TestWakeOldPrxFailsFastDetached(t *testing.T) {
	withOwner(t, "sid-live", 4242, true)
	fakePrx(t, `[ "$(ps -o pgid= -p $$ | tr -d ' ')" = "$$" ] || exit 8
if (exec </dev/tty) 2>/dev/null; then exit 9; fi
echo 'unknown command "peer"' >&2
exit 1
`)
	start := time.Now()
	_, err := (&prx{}).Wake(harness.WakeRequest{SessionID: "sid-live", Notice: "x", Timeout: 3 * time.Second})
	if err == nil || errors.Is(err, harness.ErrWakeUnavailable) {
		t.Fatalf("old prx: err = %v, want a plain push error", err)
	}
	if !strings.Contains(err.Error(), "exit status 1") || !strings.Contains(err.Error(), `unknown command "peer"`) {
		t.Fatalf("err = %v, want exit 1 with stderr", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("old prx did not fail fast: %v", time.Since(start))
	}
}

// prx `peer send` appends to the inbox and exits 0 whatever the receiver does;
// only a running peer watcher (peers.enabled) drains it and starts a turn. The
// watcher publishes .peers/<id>.json while it runs, so no record means no wake
// channel: flow stores the mail for the hooks instead of reporting "woke".
func TestWakeLiveNeedsPeerWatcher(t *testing.T) {
	withOwner(t, "sid-live", 4242, true)
	removePeerRecord(t, "sid-live")
	calls := fakeSend(t, nil)
	p := &prx{}
	req := harness.WakeRequest{SessionID: "sid-live", Notice: "x"}
	if p.WakeLive(req) {
		t.Fatal("peers off (no peer record): WakeLive = true")
	}
	if _, err := p.Wake(req); !errors.Is(err, harness.ErrWakeUnavailable) {
		t.Fatalf("peers off: err = %v, want ErrWakeUnavailable", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("send ran with no peer watcher: %v", *calls)
	}
}

// An app-chat-only session publishes inbound "refuse" and never reads its peer
// inbox. "hold" parks every message for the human's approval until it is
// released, so no turn starts from a wake: it is not live either. Only accept
// (and the default, which accepts) has a watcher that starts turns on arrival.
func TestWakeLivePeerInboundPolicy(t *testing.T) {
	for _, c := range []struct {
		inbound string
		live    bool
	}{{"", true}, {"accept", true}, {"hold", false}, {"refuse", false}, {"REFUSE", false}, {"HOLD", false}} {
		withOwner(t, "sid-live", 4242, true)
		writePeerRecord(t, "sid-live", c.inbound)
		if got := (&prx{}).WakeLive(harness.WakeRequest{SessionID: "sid-live"}); got != c.live {
			t.Errorf("inbound %q: WakeLive = %v, want %v", c.inbound, got, c.live)
		}
	}
}

// A prx that hangs and leaves a child holding stderr must not block the send
// past its timeout: the group is killed and WaitDelay bounds the pipe.
func TestPeerSendTimeoutKillsDescendantHoldingStderr(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	fakePrx(t, "sleep 30 &\necho $! > "+pidFile+"\nsleep 30\n")
	start := time.Now()
	_, err := runPeerSend([]string{"prx", "peer", "send"}, time.Second)
	if err == nil {
		t.Fatal("want timeout error")
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("send blocked %v past its 1s timeout", d)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("descendant never recorded its pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		t.Fatalf("bad descendant pid %q: %v", raw, err)
	}
	// Failure cleanup: never leave the orphan running past this test.
	t.Cleanup(func() {
		if processAlive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	// Reaping an orphan can lag the kill by a moment, so poll briefly.
	for deadline := time.Now().Add(2 * time.Second); processAlive(pid) && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Fatalf("descendant pid %d still alive after the send timed out: its process group was not killed", pid)
	}
}

// processAlive reports whether pid exists (a zombie counts as alive until reaped).
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// The push is chat from another session, so --with text must never ride it.
func TestPrxPushIsChatOnly(t *testing.T) {
	var w harness.Waker = &prx{}
	c, ok := w.(harness.ChatOnlyWaker)
	if !ok || !c.ChatOnly() {
		t.Fatal("prx push must report ChatOnly: --with text would reach the agent as a peer message")
	}
}
