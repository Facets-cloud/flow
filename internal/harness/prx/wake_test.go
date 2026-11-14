package prx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
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
