package codex

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"flow/internal/harness"
)

const wakeTID = "019a1ce3-7b2c-7d10-9c4e-0123456789ab"

func stubPS(t *testing.T, out string) {
	t.Helper()
	orig := PSRunner
	t.Cleanup(func() { PSRunner = orig })
	PSRunner = func() ([]byte, error) { return []byte(out), nil }
}

// Only an interactive TUI counts as a live wake target: queueing into a
// running `codex exec resume` (an auto run) loses the message and wedges
// the thread (codex-wake-spike case 4).
func TestWakeLiveInteractiveOnly(t *testing.T) {
	w := New().(harness.Waker)
	req := harness.WakeRequest{SessionID: wakeTID}
	stubPS(t, "  101 codex -s workspace-write -a on-request resume "+wakeTID+"\n")
	if !w.WakeLive(req) {
		t.Error("interactive TUI must be live")
	}
	stubPS(t, "  102 codex exec resume --dangerously-bypass-approvals-and-sandbox "+wakeTID+" prompt\n")
	if w.WakeLive(req) {
		t.Error("an exec (auto run) must never be a wake target")
	}
	stubPS(t, "  103 codex -s workspace-write -a on-request resume "+wakeTID+"\n"+
		"  104 codex exec resume "+wakeTID+" p\n")
	if w.WakeLive(req) {
		t.Error("TUI + live exec on the same thread: never queue")
	}
	stubPS(t, "  105 codex app-server --listen unix:// --managed-daemon\n")
	if w.WakeLive(req) {
		t.Error("a closed TUI (daemon only) is not live: the queued turn would run unwatched")
	}
}

func TestWakeQueuesNoResume(t *testing.T) {
	var got []string
	orig := QueueRunner
	t.Cleanup(func() { QueueRunner = orig })
	QueueRunner = func(argv []string, timeout time.Duration) (string, error) {
		got = argv
		return "", nil
	}
	stubPS(t, "  101 codex resume "+wakeTID+"\n")
	w := New().(harness.Waker)
	if _, err := w.Wake(harness.WakeRequest{SessionID: wakeTID, Notice: "[flow-bus wake] n", Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "queue", "--thread", wakeTID, "--message", "[flow-bus wake] n"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// rc 0 means queued, not delivered: a stderr Warning is surfaced as detail.
func TestWakeQueueWarningIsDetail(t *testing.T) {
	orig := QueueRunner
	t.Cleanup(func() { QueueRunner = orig })
	QueueRunner = func([]string, time.Duration) (string, error) {
		return "Warning: queued but could not resume thread x: start a local daemon\n", nil
	}
	stubPS(t, "  101 codex resume "+wakeTID+"\n")
	detail, err := New().(harness.Waker).Wake(harness.WakeRequest{SessionID: wakeTID, Notice: "n"})
	if err != nil || detail == "" {
		t.Fatalf("detail=%q err=%v", detail, err)
	}
	QueueRunner = func([]string, time.Duration) (string, error) { return "boom", errors.New("exit 1") }
	if _, err := New().(harness.Waker).Wake(harness.WakeRequest{SessionID: wakeTID, Notice: "n"}); err == nil {
		t.Fatal("non-zero exit must be an error")
	}
	stubPS(t, "")
	if _, err := New().(harness.Waker).Wake(harness.WakeRequest{SessionID: wakeTID, Notice: "n"}); !errors.Is(err, harness.ErrWakeUnavailable) {
		t.Fatalf("not live → ErrWakeUnavailable, got %v", err)
	}
}
