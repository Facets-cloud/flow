package app

import (
	"errors"
	"testing"
	"time"

	"flow/internal/harness"
)

// regBG is a BackgroundLauncher whose registry answers come from a script.
type regBG struct {
	harness.BackgroundLauncher
	replies []func() ([]harness.BackgroundAgent, error)
	calls   int
	stopped int
}

func (r *regBG) BackgroundAgents() ([]harness.BackgroundAgent, error) {
	f := r.replies[min(r.calls, len(r.replies)-1)]
	r.calls++
	return f()
}
func (r *regBG) StopBackground(string) error { r.stopped++; return nil }

func regErr() ([]harness.BackgroundAgent, error)  { return nil, errors.New("registry down") }
func regGone() ([]harness.BackgroundAgent, error) { return nil, nil }

func fastStop(t *testing.T) {
	t.Helper()
	oldT, oldP := bgStopWaitTimeout, bgStopWaitPoll
	bgStopWaitTimeout, bgStopWaitPoll = 50*time.Millisecond, time.Millisecond
	t.Cleanup(func() { bgStopWaitTimeout, bgStopWaitPoll = oldT, oldP })
}

func TestWaitBgStoppedRegistryError(t *testing.T) {
	fastStop(t)
	r := &regBG{replies: []func() ([]harness.BackgroundAgent, error){regErr, regErr, regGone}}
	if !waitBgStopped(r, "sid") || r.calls != 3 {
		t.Errorf("transient error: calls=%d, want keep polling until a clean listing", r.calls)
	}
	r = &regBG{replies: []func() ([]harness.BackgroundAgent, error){regErr}}
	if waitBgStopped(r, "sid") {
		t.Error("persistent registry error reported as exited")
	}
}

func TestStopForkedOriginalRequiresProvablyIdle(t *testing.T) {
	// idle by coarse status but activity unknown → not provably idle.
	a := harness.BackgroundAgent{ShortID: "abcd1234", SessionID: "sid", PID: 42, Status: "idle"}
	r := &regBG{replies: []func() ([]harness.BackgroundAgent, error){
		func() ([]harness.BackgroundAgent, error) { return []harness.BackgroundAgent{a}, nil }}}
	stopForkedOriginal(r, "t", "sid")
	if r.stopped != 0 {
		t.Fatal("not-provably-idle original was stopped")
	}
	a.Activity = &harness.BackgroundActivity{}
	stopForkedOriginal(r, "t", "sid")
	if r.stopped != 1 {
		t.Fatal("provably idle original was not stopped")
	}
}

func TestRefreshHintHonoursKillSwitch(t *testing.T) {
	t.Setenv(selfUpgradeEnv, "")
	if got := refreshHint(); got != "The next flow command refreshes the skill and the hooks." {
		t.Errorf("default: %q", got)
	}
	t.Setenv(selfUpgradeEnv, "1")
	if got := refreshHint(); got != "Run `flow skill update` to refresh the skill and the hooks." {
		t.Errorf("kill switch: %q", got)
	}
}
