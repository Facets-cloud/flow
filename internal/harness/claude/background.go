package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

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
	// The read-only `agents` query is bounded so a stalled claude daemon
	// can't hang flow show/list forever — but the cap is generous: this
	// same query is how SpawnBackground/ResumeBackground capture the
	// minted session id, and immediately after a `--bg` launch the daemon
	// is busy registering the new agent, so the query routinely takes
	// ~10s (measured). The timeout is a backstop against a true hang, not
	// a tight latency bound; too-tight a value kills legitimate captures.
	if len(args) >= 1 && args[0] == "agents" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "claude", args...)
		if workDir != "" {
			cmd.Dir = workDir
		}
		return cmd.CombinedOutput()
	}
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
// "backgrounded" marker on the banner line. Anchored to line start so a
// help line that merely mentions the short id can never match.
var bannerShortIDRe = regexp.MustCompile(`^\s*backgrounded\s*·?\s*([0-9a-f]{8})\b`)

// parseBackgroundBanner extracts the short id from `claude --bg`'s banner
// (`backgrounded · <shortId> · <name>`). Older CLIs print the banner as
// the first line; newer ones print a "Starting background service…"
// preamble first, so every line is scanned and the first one that starts
// with the "backgrounded" marker wins. The help lines beneath it also
// mention the short id but never carry the marker. Returns an error if no
// banner line is present (e.g. the spawn failed before registering).
func parseBackgroundBanner(out string) (string, error) {
	clean := ansiRe.ReplaceAllString(out, "")
	for _, line := range strings.Split(clean, "\n") {
		if m := bannerShortIDRe.FindStringSubmatch(line); m != nil {
			return m[1], nil
		}
	}
	return "", fmt.Errorf("no background banner in output: %q", strings.TrimSpace(clean))
}

// bgAgentJSON mirrors one element of `claude agents --json`.
type bgAgentJSON struct {
	PID       int    `json:"pid"`
	ID        string `json:"id"`
	Cwd       string `json:"cwd"`
	Kind      string `json:"kind"`
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	State     string `json:"state"`
}

// parseBackgroundAgents decodes `claude agents --json` into the harness's
// normalized BackgroundAgent slice. Only `kind:"background"` entries are
// returned — `--all` also lists interactive (terminal-tab) sessions, and
// counting those would mislabel an ordinary `flow do` tab session as a
// background agent in flow show / list.
func parseBackgroundAgents(raw []byte) ([]harness.BackgroundAgent, error) {
	var entries []bgAgentJSON
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse claude agents --json: %w", err)
	}
	out := make([]harness.BackgroundAgent, 0, len(entries))
	for _, e := range entries {
		if e.Kind != "background" {
			continue
		}
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
	agents, err := parseBackgroundAgents(out)
	if err != nil {
		return nil, err
	}
	for i := range agents {
		if agents[i].State != "failed" && agents[i].PID <= 0 {
			continue
		}
		st, ok := readJobState(agents[i].ShortID)
		if !ok {
			continue
		}
		if agents[i].State == "failed" {
			agents[i].Detail = st.Detail
		}
		if agents[i].PID > 0 {
			agents[i].Activity = st.activity()
		}
	}
	return agents, nil
}

// jobState mirrors the parts of ~/.claude/jobs/<shortID>/state.json flow
// reads. `claude agents --json` omits all of them.
type jobState struct {
	State    string          `json:"state"`
	Tempo    string          `json:"tempo"`
	Detail   string          `json:"detail"` // why a failed job died ("source session … not found")
	Needs    json.RawMessage `json:"needs"`
	NeedsAsk json.RawMessage `json:"needsAsk"`
	InFlight *struct {
		Tasks             int `json:"tasks"`
		Queued            int `json:"queued"`
		DrainableMonitors int `json:"drainableMonitors"`
	} `json:"inFlight"`
}

// activity reports what the live job is doing between turns. Verified on
// 2.1.325: a session waiting on the user is state/tempo "blocked" with
// needs/needsAsk set (its registry status is still "idle"); background
// shells and monitors are counted under inFlight. A state file with no
// inFlight record is treated as unknown (nil).
func (st jobState) activity() *harness.BackgroundActivity {
	if st.InFlight == nil {
		return nil
	}
	return &harness.BackgroundActivity{
		NeedsInput: st.State == "blocked" || st.Tempo == "blocked" ||
			jsonSet(st.Needs) || jsonSet(st.NeedsAsk),
		InFlight: st.InFlight.Tasks + st.InFlight.Queued + st.InFlight.DrainableMonitors,
	}
}

// jsonSet reports whether a raw JSON field is present and not null/"".
func jsonSet(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != `""`
}

// readJobState reads ~/.claude/jobs/<shortID>/state.json.
func readJobState(shortID string) (jobState, bool) {
	home, err := os.UserHomeDir()
	if err != nil || shortID == "" {
		return jobState{}, false
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "jobs", filepath.Base(shortID), "state.json"))
	if err != nil {
		return jobState{}, false
	}
	var st jobState
	if json.Unmarshal(raw, &st) != nil {
		return jobState{}, false
	}
	return st, true
}

// StopBackground runs `claude stop <shortID>`.
func (c *claude) StopBackground(shortID string) error {
	out, err := BGCommandRunner("", []string{"stop", shortID})
	if err != nil {
		return fmt.Errorf("claude stop %s: %w (output: %s)", shortID, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// BackgroundForkSource reads the live agent's command line: `claude --bg
// --resume <src>` runs as `… --session-id <new> --fork-session --resume
// <src> …`, so <src> is where the conversation's history lives.
func (c *claude) BackgroundForkSource(a harness.BackgroundAgent) (string, bool) {
	if a.PID <= 0 {
		return "", false
	}
	out, err := PSRunner()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != strconv.Itoa(a.PID) {
			continue
		}
		fork := false
		for _, arg := range f[1:] {
			if arg == "--fork-session" {
				fork = true
			}
		}
		for i := 1; fork && i+1 < len(f); i++ {
			if f[i] == "--resume" {
				return f[i+1], true
			}
		}
		return "", false
	}
	return "", false
}

// launchAndCapture runs a `claude --bg …` command in workDir, parses the
// short id from its banner, and resolves the full session id by matching
// that short id in the agent registry. One deterministic lookup — no
// polling, no race — because `--bg` only prints the banner after the
// session is registered. Shared by SpawnBackground and ResumeBackground
// (which differ only in their argv; a resume may keep the id or start a copy).
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
	args := []string{"--bg", "--name", name, harness.WithInjection(prompt, opts.Inject)}
	if opts.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	return c.launchAndCapture(workDir, args, "spawn")
}

// ResumeBackground runs `claude --bg [--name <name>] --resume <sessionID>
// [<prompt>] [--dangerously-skip-permissions]`. opts.Inject is appended to
// prompt behind InjectionMarker, as in SpawnBackground.
//
// When claude keeps the id vs starts a copy (verified live, 2.1.325):
//
//	session                              flags   result
//	alive (busy or idle)                 any     copy, new id ("already running")
//	registered in Agent View, exited     none    SAME id, saved options reapplied
//	registered in Agent View, exited     some    copy ("keeps its own saved options")
//	unregistered (rm'd / never bg)       any     SAME id
//
// So with opts.ResumeRegistered no flags are passed: the registry's saved
// --name and permission mode come back with the same session. Otherwise
// --name keeps the display name SpawnBackground used (without it claude
// names the session after the prompt). Either way the id that came up is
// captured from the banner and returned; the caller records it.
func (c *claude) ResumeBackground(workDir, name, sessionID, prompt string, opts harness.LaunchOpts) (harness.BackgroundAgent, error) {
	args := []string{"--bg"}
	if !opts.ResumeRegistered {
		args = append(args, "--name", name)
	}
	args = append(args, "--resume", sessionID)
	if p := harness.WithInjection(prompt, opts.Inject); p != "" {
		args = append(args, p)
	}
	if opts.SkipPermissions && !opts.ResumeRegistered {
		args = append(args, "--dangerously-skip-permissions")
	}
	return c.launchAndCapture(workDir, args, "resume")
}
