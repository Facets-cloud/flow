package claude

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"flow/internal/harness"
)

// BGCommandRunner executes `claude <args...>` and returns its combined
// output. It is the single test seam for all background operations
// (spawn, resume, list): tests dispatch on args to return canned
// banners / JSON without spawning a real claude. The default execs the
// real binary; because Go's exec invokes the binary directly (NOT via a
// shell), the user's interactive `claude` alias — which injects --bg and
// breaks --session-id pinning — never applies here. flow controls every
// flag.
var BGCommandRunner = runBGCommand

func runBGCommand(args []string) ([]byte, error) {
	return exec.Command("claude", args...).CombinedOutput()
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

// BackgroundAgents runs `claude agents --json` and decodes the registry.
func (c *claude) BackgroundAgents() ([]harness.BackgroundAgent, error) {
	out, err := BGCommandRunner([]string{"agents", "--json"})
	if err != nil {
		return nil, fmt.Errorf("claude agents --json: %w", err)
	}
	return parseBackgroundAgents(out)
}

// SpawnBackground runs `claude --bg --name <name> <prompt>
// [--dangerously-skip-permissions]`, parses the short id from the banner,
// then resolves the full session id by matching that short id in the
// agent registry (one deterministic lookup — no polling, no race, since
// the banner only prints after the session is registered). opts.Inject is
// appended to the prompt behind InjectionMarker, mirroring LaunchCmd.
func (c *claude) SpawnBackground(name, prompt string, opts harness.LaunchOpts) (harness.BackgroundAgent, error) {
	if opts.Inject != "" {
		prompt = prompt + "\n\n" + harness.InjectionMarker + "\n" + opts.Inject
	}
	args := []string{"--bg", "--name", name, prompt}
	if opts.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	out, err := BGCommandRunner(args)
	if err != nil {
		return harness.BackgroundAgent{}, fmt.Errorf("claude --bg: %w (output: %s)", err, strings.TrimSpace(string(out)))
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
		"spawned background agent %s but it is not in `claude agents --json` — cannot capture its session id", shortID)
}

// ResumeBackground runs `claude --bg --resume <sessionID>
// [<injection>] [--dangerously-skip-permissions]`, continuing the same
// session (id and transcript preserved). Output is ignored — only the
// exit status matters.
func (c *claude) ResumeBackground(sessionID string, opts harness.LaunchOpts) error {
	args := []string{"--bg", "--resume", sessionID}
	if opts.Inject != "" {
		args = append(args, harness.InjectionMarker+"\n"+opts.Inject)
	}
	if opts.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	if out, err := BGCommandRunner(args); err != nil {
		return fmt.Errorf("claude --bg --resume %s: %w (output: %s)", sessionID, err, strings.TrimSpace(string(out)))
	}
	return nil
}
