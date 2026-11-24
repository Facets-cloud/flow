package codex

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"flow/internal/harness"
)

var _ harness.BackgroundLauncher = (*codex)(nil)

type tmuxCall struct{ args []string }

// stubTmux records tmux invocations and answers list-panes with listOut.
func stubTmux(t *testing.T, listOut string, listErr error) *[]tmuxCall {
	t.Helper()
	calls := &[]tmuxCall{}
	oldRun, oldLook := TmuxRunner, TmuxLookPath
	TmuxRunner = func(args []string) ([]byte, error) {
		*calls = append(*calls, tmuxCall{args})
		if len(args) > 0 && args[0] == "list-panes" {
			return []byte(listOut), listErr
		}
		return nil, nil
	}
	TmuxLookPath = func(string) (string, error) { return "/usr/bin/tmux", nil }
	t.Cleanup(func() { TmuxRunner, TmuxLookPath = oldRun, oldLook })
	return calls
}

func stubProbe(t *testing.T, sid string) {
	t.Helper()
	old := ProbeRunner
	ProbeRunner = func(string, harness.LaunchOpts) ([]byte, error) {
		return []byte(`{"type":"thread.started","thread_id":"` + sid + `"}`), nil
	}
	t.Cleanup(func() { ProbeRunner = old })
}

func newSessionArgs(t *testing.T, calls *[]tmuxCall) []string {
	t.Helper()
	for _, c := range *calls {
		if len(c.args) > 0 && c.args[0] == "new-session" {
			return c.args
		}
	}
	t.Fatalf("no tmux new-session call: %v", *calls)
	return nil
}

func argAfter(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

// SpawnBackground mints a thread, then runs the interactive Codex TUI on it
// in a detached tmux session named after the thread, in the work dir, with
// flow's env carried in and hook trust bypassed (nobody approves a prompt).
func TestSpawnBackgroundDetachedTmux(t *testing.T) {
	t.Setenv("FLOW_ROOT", "/tmp/flowroot")
	stubProbe(t, testThreadID)
	calls := stubTmux(t, "", nil)

	a, err := New().(*codex).SpawnBackground("/w/task", "proj/task", "boot prompt",
		harness.LaunchOpts{SkipPermissions: true, Inject: "do X", TrustDir: "/w/task"})
	if err != nil {
		t.Fatal(err)
	}
	if a.SessionID != testThreadID || a.ShortID != testThreadID[:8] {
		t.Errorf("agent = %+v", a)
	}
	args := newSessionArgs(t, calls)
	if !containsArg(args, "-d") || argAfter(args, "-s") != "flow-codex-"+testThreadID || argAfter(args, "-c") != "/w/task" {
		t.Errorf("new-session args = %v", args)
	}
	if !containsArg(args, "FLOW_ROOT=/tmp/flowroot") {
		t.Errorf("FLOW_ROOT not carried into the tmux session: %v", args)
	}
	cmd := args[len(args)-1]
	for _, want := range []string{"codex --dangerously-bypass-approvals-and-sandbox", hookTrustBypass, " resume", testThreadID, "boot prompt", "do X", "trust_level"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command missing %q: %s", want, cmd)
		}
	}
}

func TestSpawnBackgroundNeedsTmux(t *testing.T) {
	stubProbe(t, testThreadID)
	stubTmux(t, "", nil)
	TmuxLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	_, err := New().(*codex).SpawnBackground("/w", "n", "p", harness.LaunchOpts{})
	if err == nil || !strings.Contains(err.Error(), "tmux") {
		t.Errorf("err = %v, want a tmux-required error", err)
	}
}

// ResumeBackground keeps the SAME thread: no probe, `resume <id> <kickoff>`.
func TestResumeBackgroundSameThread(t *testing.T) {
	old := ProbeRunner
	ProbeRunner = func(string, harness.LaunchOpts) ([]byte, error) {
		t.Fatal("resume must not mint a new thread")
		return nil, nil
	}
	t.Cleanup(func() { ProbeRunner = old })
	calls := stubTmux(t, "", nil)
	a, err := New().(*codex).ResumeBackground("/w", "n", testThreadID, "kick off", harness.LaunchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if a.SessionID != testThreadID {
		t.Errorf("agent = %+v", a)
	}
	cmd := newSessionArgs(t, calls)
	last := cmd[len(cmd)-1]
	if !strings.Contains(last, "codex -s workspace-write -a on-request") || !strings.Contains(last, "resume "+testThreadID) || !strings.Contains(last, "kick off") {
		t.Errorf("resume command = %s", last)
	}
}

// BackgroundAgents lists flow's Codex tmux sessions (live panes); other
// tmux sessions are ignored and "no server running" means none.
func TestBackgroundAgentsFromTmux(t *testing.T) {
	out := "flow-codex-" + testThreadID + "\t4242\t/w\tproj/task\n" +
		"work\t99\t/home\tbash\n"
	stubTmux(t, out, nil)
	agents, err := New().(*codex).BackgroundAgents()
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Fatalf("agents = %+v", agents)
	}
	a := agents[0]
	if a.SessionID != testThreadID || a.PID != 4242 || a.Cwd != "/w" || a.Name != "proj/task" || a.ShortID != testThreadID[:8] {
		t.Errorf("agent = %+v", a)
	}
	if a.Status != "idle" {
		t.Errorf("status = %q (a live Codex TUI queues pushed mail, so it reads as idle for delivery)", a.Status)
	}

	stubTmux(t, "", errors.New("no server running on /tmp/tmux-501/default"))
	if agents, err := New().(*codex).BackgroundAgents(); err != nil || len(agents) != 0 {
		t.Errorf("no server: agents=%v err=%v, want none", agents, err)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// A new tmux session inherits the tmux SERVER's environment, which may
// hold another harness's session id (e.g. a Claude session started the
// server). Two harness ids make flow's session lookup ambiguous, so the
// launch unsets every harness session marker before starting Codex.
func TestBgCommandClearsForeignSessionEnv(t *testing.T) {
	cmd := bgCommand(testThreadID, "p", harness.LaunchOpts{})
	if !strings.HasPrefix(cmd, "env ") {
		t.Fatalf("command should start with env: %s", cmd)
	}
	for _, k := range []string{"CLAUDE_CODE_SESSION_ID", "PRAXIS_SESSION_ID", "CODEX_THREAD_ID", "FLOW_AUTO_RUN"} {
		if !strings.Contains(cmd, "-u "+k+" ") {
			t.Errorf("command does not unset %s: %s", k, cmd)
		}
	}
}
