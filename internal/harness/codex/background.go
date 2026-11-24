package codex

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"flow/internal/harness"
	"flow/internal/spawner"
)

// Codex background sessions (`flow do --bg` / $FLOW_TERM=bg).
//
// Codex has no background-agent registry of its own, so flow hosts the
// interactive Codex TUI in a DETACHED tmux session, one per thread:
//
//	tmux new-session -d -s flow-codex-<thread> -c <work_dir> -n <title> \
//	     -e FLOW_ROOT=… -e PATH=… '<codex … resume <thread> <prompt>>'
//
// The TUI is a live process holding the thread, so everything flow already
// does for a live Codex session works unchanged: LiveSessionIDs sees it,
// and wake pushes mail with `codex queue --thread` (it runs as the next
// turn). The tmux session ends when Codex exits; `tmux attach -t
// flow-codex-<thread>` watches or drives it.

// tmuxSessionPrefix names flow's Codex tmux sessions; the thread id
// follows, so the registry needs no state of its own.
const tmuxSessionPrefix = "flow-codex-"

var (
	// TmuxRunner runs tmux with args and returns its combined output.
	// Tests replace it.
	TmuxRunner = func(args []string) ([]byte, error) {
		return exec.Command("tmux", args...).CombinedOutput()
	}
	// TmuxLookPath probes for the tmux binary. Tests replace it.
	TmuxLookPath = exec.LookPath
)

// tmuxSessionName is the tmux session hosting thread sessionID.
func tmuxSessionName(sessionID string) string { return tmuxSessionPrefix + sessionID }

// TmuxSessionName is exported for attach hints.
func TmuxSessionName(sessionID string) string { return tmuxSessionName(sessionID) }

// bgEnvKeys are carried into the tmux session explicitly: a new session
// inherits the tmux SERVER's environment, not the caller's, so without
// them a session started under a non-default FLOW_ROOT (or PATH) would
// talk to the wrong flow.
var bgEnvKeys = []string{"FLOW_ROOT", "PATH", "CODEX_HOME"}

// bgEnvReset clears harness session markers before Codex starts. A new
// tmux session inherits the tmux SERVER's environment, which can hold
// another harness's session id (a Claude session that started the server
// leaves CLAUDE_CODE_SESSION_ID there; verified live). With two harness
// ids set, flow cannot tell which session it is in, so the Codex session
// would run unbound (its inbox pops as the human's). FLOW_AUTO_RUN is
// cleared too: a background session is never an auto run.
const bgEnvReset = "env -u CLAUDE_CODE_SESSION_ID -u CLAUDECODE -u PRAXIS_SESSION_ID -u CODEX_THREAD_ID -u FLOW_AUTO_RUN "

// bgCommand is the interactive Codex launch on sessionID. Like the tab
// launch, plus hook-trust bypass: nobody is at the TUI to approve a
// freshly installed hook, so flow's hooks would otherwise not fire.
func bgCommand(sessionID, prompt string, opts harness.LaunchOpts) string {
	if opts.Inject != "" {
		if prompt != "" {
			prompt += "\n\n"
		}
		prompt += harness.InjectionMarker + "\n" + opts.Inject
	}
	cmd := bgEnvReset + interactivePrefix(opts) + " " + hookTrustBypass + trustShell(opts.TrustDir) +
		" resume" + modelShell(opts) + " " + sessionID
	if prompt != "" {
		cmd += " " + spawner.ShellQuote(prompt)
	}
	return cmd
}

func shortID(sessionID string) string {
	if len(sessionID) >= 8 {
		return sessionID[:8]
	}
	return sessionID
}

// startTmux opens the detached tmux session for sessionID.
func startTmux(workDir, name, sessionID, command string) (harness.BackgroundAgent, error) {
	if _, err := TmuxLookPath("tmux"); err != nil {
		return harness.BackgroundAgent{}, fmt.Errorf("codex background sessions run in a detached tmux session, and tmux is not installed (brew install tmux / apt install tmux): %w", err)
	}
	args := []string{"new-session", "-d", "-s", tmuxSessionName(sessionID), "-c", workDir}
	if name != "" {
		args = append(args, "-n", name)
	}
	for _, k := range bgEnvKeys {
		if v, ok := os.LookupEnv(k); ok {
			args = append(args, "-e", k+"="+v)
		}
	}
	args = append(args, command)
	if out, err := TmuxRunner(args); err != nil {
		return harness.BackgroundAgent{}, fmt.Errorf("tmux new-session for codex thread %s: %w: %s", sessionID, err, strings.TrimSpace(string(out)))
	}
	return harness.BackgroundAgent{
		ShortID: shortID(sessionID), SessionID: sessionID, Name: name, Cwd: workDir,
		Status: "idle", State: "running",
	}, nil
}

// SpawnBackground mints a thread (the same probe as a tab launch), then
// opens the Codex TUI on it in a detached tmux session.
func (c *codex) SpawnBackground(workDir, name, prompt string, opts harness.LaunchOpts) (harness.BackgroundAgent, error) {
	if _, err := TmuxLookPath("tmux"); err != nil {
		return harness.BackgroundAgent{}, fmt.Errorf("codex background sessions run in a detached tmux session, and tmux is not installed (brew install tmux / apt install tmux): %w", err)
	}
	sid, err := c.NewSessionID(workDir, opts)
	if err != nil {
		return harness.BackgroundAgent{}, err
	}
	return startTmux(workDir, name, sid, bgCommand(sid, prompt, opts))
}

// ResumeBackground reopens the SAME thread (Codex never copies on resume)
// in a fresh detached tmux session, with prompt as its first turn.
func (c *codex) ResumeBackground(workDir, name, sessionID, prompt string, opts harness.LaunchOpts) (harness.BackgroundAgent, error) {
	return startTmux(workDir, name, sessionID, bgCommand(sessionID, prompt, opts))
}

// BackgroundAgents lists flow's live Codex tmux sessions. Status is
// "idle" for every live one: Codex queues a pushed message until the
// running turn ends, so flow may always deliver to it. No tmux server (or
// no tmux) means no sessions.
func (c *codex) BackgroundAgents() ([]harness.BackgroundAgent, error) {
	if _, err := TmuxLookPath("tmux"); err != nil {
		return nil, nil
	}
	out, err := TmuxRunner([]string{"list-panes", "-a", "-F",
		"#{session_name}\t#{pane_pid}\t#{pane_current_path}\t#{window_name}"})
	if err != nil {
		msg := err.Error() + " " + string(out)
		if strings.Contains(msg, "no server running") || strings.Contains(msg, "error connecting") {
			return nil, nil
		}
		return nil, fmt.Errorf("tmux list-panes: %w", err)
	}
	var agents []harness.BackgroundAgent
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 4 || !strings.HasPrefix(f[0], tmuxSessionPrefix) {
			continue
		}
		sid := strings.TrimPrefix(f[0], tmuxSessionPrefix)
		if seen[sid] {
			continue
		}
		seen[sid] = true
		pid, _ := strconv.Atoi(f[1])
		agents = append(agents, harness.BackgroundAgent{
			ShortID: shortID(sid), SessionID: sid, Name: f[3], Cwd: f[2],
			PID: pid, Status: "idle", State: "running",
		})
	}
	return agents, nil
}
