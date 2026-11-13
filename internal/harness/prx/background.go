package prx

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"flow/internal/harness"
)

// prx must keep satisfying the optional background capability; flow only
// discovers it with a type assertion, so a signature drift would otherwise
// compile and silently drop background support.
var _ harness.BackgroundLauncher = (*prx)(nil)

// BGCommandRunner runs `prx <args...>` in workDir and returns stdout. It is
// the single test seam for background operations (spawn, resume, list).
// Empty workDir inherits flow's cwd (used for the listing).
var BGCommandRunner = runBGCommand

func runBGCommand(workDir string, args []string) ([]byte, error) {
	// A detached start returns once the host is up; the listing reads the
	// session store. Neither should take long, so cap both against a hang.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "prx", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return out, err
}

// SpawnBackground starts a detached prx host (`prx run -sdk -detach`) in
// workDir running prompt, and returns the session id prx minted. The host
// stays up after the prompt so the session can be attached with
// `prx -resume <id>`. name is unused: prx's -name is a peer name, not a
// display title.
func (p *prx) SpawnBackground(workDir, name, prompt string, opts harness.LaunchOpts) (harness.BackgroundAgent, error) {
	if opts.Inject != "" {
		prompt += "\n\n" + harness.InjectionMarker + "\n" + opts.Inject
	}
	args := append(detachArgs(workDir, opts), "-prompt", prompt)
	return p.detach(workDir, args, "spawn")
}

// ResumeBackground restarts a detached host on an existing session. Unlike
// `claude --bg --resume`, prx keeps the session id. name is unused, as in
// SpawnBackground.
func (p *prx) ResumeBackground(workDir, name, sessionID string, opts harness.LaunchOpts) (harness.BackgroundAgent, error) {
	args := append(detachArgs(workDir, opts), "-session", sessionID)
	if opts.Inject != "" {
		args = append(args, "-prompt", harness.InjectionMarker+"\n"+opts.Inject)
	}
	return p.detach(workDir, args, "resume")
}

func detachArgs(workDir string, opts harness.LaunchOpts) []string {
	args := []string{"run", "-sdk", "-detach", "-cwd", workDir, "-max-turns", maxTurns}
	if opts.SkipPermissions {
		args = append(args, "-permission-mode", bypass)
	}
	return args
}

// detach runs a `prx run -sdk -detach` command and decodes the one JSON line
// it prints: {"sessionId","endpoint","pid","log"}.
func (p *prx) detach(workDir string, args []string, what string) (harness.BackgroundAgent, error) {
	out, err := BGCommandRunner(workDir, args)
	if err != nil {
		return harness.BackgroundAgent{}, fmt.Errorf("prx run -detach (%s): %w", what, err)
	}
	var host struct {
		SessionID string `json:"sessionId"`
		PID       int    `json:"pid"`
	}
	if err := json.Unmarshal(lastJSONLine(out), &host); err != nil || host.SessionID == "" {
		return harness.BackgroundAgent{}, fmt.Errorf("prx run -detach (%s): no session id in output: %q", what, strings.TrimSpace(string(out)))
	}
	return harness.BackgroundAgent{
		ShortID:   shortID(host.SessionID),
		SessionID: host.SessionID,
		Cwd:       workDir,
		PID:       host.PID,
		Status:    "running",
	}, nil
}

func lastJSONLine(out []byte) []byte {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); strings.HasPrefix(l, "{") {
			return []byte(l)
		}
	}
	return nil
}

// shortID is the display handle for a session: its LAST 8 characters. prx
// mints UUIDv7, whose leading characters are a timestamp, so sessions started
// close together share a prefix but not a suffix.
func shortID(id string) string {
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

// BackgroundAgents reads `prx run agents -json`, which is read-only from
// prx v0.6.56 (earlier versions pruned stale owner records while listing).
// Every session is returned; PID is the owner's pid while one holds the
// session (a detached host or an open TUI) and 0 otherwise, so flow resumes
// an exited session rather than reporting it open.
func (p *prx) BackgroundAgents() ([]harness.BackgroundAgent, error) {
	out, err := BGCommandRunner("", []string{"run", "agents", "-json"})
	if err != nil {
		return nil, fmt.Errorf("prx run agents -json: %w", err)
	}
	var rows []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Cwd    string `json:"cwd"`
		Owner  *struct {
			PID int `json:"pid"`
		} `json:"owner"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse prx run agents -json: %w", err)
	}
	agents := make([]harness.BackgroundAgent, 0, len(rows))
	listed := map[string]bool{}
	for _, r := range rows {
		a := harness.BackgroundAgent{ShortID: shortID(r.ID), SessionID: r.ID, Name: r.Name, Cwd: r.Cwd, Status: r.Status}
		if r.Owner != nil {
			a.PID = r.Owner.PID
		}
		agents = append(agents, a)
		listed[strings.ToLower(r.ID)] = true
	}
	return append(agents, unlistedOwned(listed)...), nil
}

// unlistedOwned returns live owned sessions the listing left out. prx's
// listing hides empty sessions, so a host started a moment ago has an owner
// record but no row yet; without it flow would try to resume a running
// session and prx would refuse it as already owned.
func unlistedOwned(listed map[string]bool) []harness.BackgroundAgent {
	records, err := readOwnerRecords()
	if err != nil {
		return nil
	}
	host, _ := Hostname()
	var out []harness.BackgroundAgent
	for _, o := range records {
		if listed[strings.ToLower(o.SessionID)] || !o.live(host) {
			continue
		}
		out = append(out, harness.BackgroundAgent{ShortID: shortID(o.SessionID), SessionID: o.SessionID, PID: o.PID, Status: "starting"})
	}
	return out
}
