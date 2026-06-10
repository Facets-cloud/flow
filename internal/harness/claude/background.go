package claude

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"flow/internal/harness"
)

// BGCommandRunner executes `claude <args...>` in workDir and returns its
// combined output. It is the single test seam for all background
// operations (spawn, resume, list): tests dispatch on args to return
// canned banners / JSON without spawning a real claude. The default
// execs the real binary; because Go's exec invokes the binary directly
// (NOT via a shell), the user's interactive `claude` alias — which
// injects --bg and breaks --session-id pinning — never applies here.
// flow controls every flag.
//
// workDir sets the spawned process's cwd: a `claude --bg` session begins
// there (and keys its transcript/CLAUDE.md context to it), so it must be
// the task's work_dir. Empty workDir means "inherit flow's cwd" — used
// for cwd-independent queries like `claude agents --json`.
var BGCommandRunner = runBGCommand

func runBGCommand(workDir string, args []string) ([]byte, error) {
	cmd := exec.Command("claude", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	return cmd.CombinedOutput()
}

// ansiRe strips ANSI SGR escape sequences (color/dim) that `claude --bg`
// wraps around the short id when stdout is a TTY. Stripping first lets
// the banner parser work whether or not color is present.
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// bannerShortIDRe matches the 8-char lowercase-hex short id following the
// "backgrounded" marker on the banner's first line.
var bannerShortIDRe = regexp.MustCompile(`backgrounded\s*·?\s*([0-9a-f]{8})\b`)

// parseBackgroundBanner extracts the short id from `claude --bg`'s banner
// (`backgrounded · <shortId> · <name>`). Reads only the first line — the
// help lines beneath it also mention the short id. Returns an error if no
// banner line is present (e.g. the spawn failed before registering).
func parseBackgroundBanner(out string) (string, error) {
	clean := ansiRe.ReplaceAllString(out, "")
	firstLine := clean
	if i := strings.IndexByte(clean, '\n'); i >= 0 {
		firstLine = clean[:i]
	}
	m := bannerShortIDRe.FindStringSubmatch(firstLine)
	if m == nil {
		return "", fmt.Errorf("no background banner in output: %q", strings.TrimSpace(clean))
	}
	return m[1], nil
}

// bgAgentJSON mirrors one element of `claude agents --json`.
type bgAgentJSON struct {
	PID       int    `json:"pid"`
	ID        string `json:"id"`
	Cwd       string `json:"cwd"`
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	State     string `json:"state"`
}

// parseBackgroundAgents decodes `claude agents --json` into the harness's
// normalized BackgroundAgent slice.
func parseBackgroundAgents(raw []byte) ([]harness.BackgroundAgent, error) {
	var entries []bgAgentJSON
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse claude agents --json: %w", err)
	}
	out := make([]harness.BackgroundAgent, 0, len(entries))
	for _, e := range entries {
		out = append(out, harness.BackgroundAgent{
			ShortID:   e.ID,
			SessionID: e.SessionID,
			Name:      e.Name,
			Cwd:       e.Cwd,
			PID:       e.PID,
			Status:    e.Status,
			State:     e.State,
		})
	}
	return out, nil
}

// BackgroundAgents runs `claude agents --json --all` and decodes the
// registry. --all includes exited / failed / completed sessions (not
// just live ones) so flow can tell "session removed" apart from "session
// present but not currently running" — the former needs a resume, the
// latter just needs the user to attach in the Agent View.
func (c *claude) BackgroundAgents() ([]harness.BackgroundAgent, error) {
	out, err := BGCommandRunner("", []string{"agents", "--json", "--all"})
	if err != nil {
		return nil, fmt.Errorf("claude agents --json --all: %w", err)
	}
	return parseBackgroundAgents(out)
}

// launchAndCapture runs a `claude --bg …` command in workDir, parses the
// short id from its banner, and resolves the full session id by matching
// that short id in the agent registry. One deterministic lookup — no
// polling, no race — because `--bg` only prints the banner after the
// session is registered. Shared by SpawnBackground and ResumeBackground
// (which differ only in their argv; --bg mints a fresh id either way).
func (c *claude) launchAndCapture(workDir string, args []string, what string) (harness.BackgroundAgent, error) {
	out, err := BGCommandRunner(workDir, args)
	if err != nil {
		return harness.BackgroundAgent{}, fmt.Errorf("claude --bg (%s): %w (output: %s)", what, err, strings.TrimSpace(string(out)))
	}
	shortID, err := parseBackgroundBanner(string(out))
	if err != nil {
		return harness.BackgroundAgent{}, err
	}
	agents, err := c.BackgroundAgents()
	if err != nil {
		return harness.BackgroundAgent{}, fmt.Errorf("resolve session id for %s: %w", shortID, err)
	}
	for _, a := range agents {
		if a.ShortID == shortID {
			return a, nil
		}
	}
	return harness.BackgroundAgent{}, fmt.Errorf(
		"backgrounded agent %s but it is not in `claude agents --json --all` — cannot capture its session id", shortID)
}

// SpawnBackground runs `claude --bg --name <name> <prompt>
// [--dangerously-skip-permissions]` and captures the minted session id.
// opts.Inject is appended to the prompt behind InjectionMarker, mirroring
// LaunchCmd.
func (c *claude) SpawnBackground(workDir, name, prompt string, opts harness.LaunchOpts) (harness.BackgroundAgent, error) {
	if opts.Inject != "" {
		prompt = prompt + "\n\n" + harness.InjectionMarker + "\n" + opts.Inject
	}
	args := []string{"--bg", "--name", name, prompt}
	if opts.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	return c.launchAndCapture(workDir, args, "spawn")
}

// ResumeBackground runs `claude --bg --resume <sessionID>
// [<injection>] [--dangerously-skip-permissions]`. `--bg` does not honor
// `--resume`'s id — it starts a fresh background process that *inherits
// the prior conversation* under a NEW id (verified against the CLI). So
// this captures and returns the new id exactly like SpawnBackground; the
// caller re-records it on the task.
func (c *claude) ResumeBackground(workDir, sessionID string, opts harness.LaunchOpts) (harness.BackgroundAgent, error) {
	args := []string{"--bg", "--resume", sessionID}
	if opts.Inject != "" {
		args = append(args, harness.InjectionMarker+"\n"+opts.Inject)
	}
	if opts.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	return c.launchAndCapture(workDir, args, "resume")
}
