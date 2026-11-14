// Package prx implements harness.Harness for praxis-harness (the `prx` CLI).
//
// prx has no CLI for locating, listing or rendering sessions, so this
// adapter reads prx's on-disk state directly: transcripts at
// <sessionDir>/<id>/session.jsonl (gzipped to session.jsonl.gz once idle)
// and owner records at <sessionDir>/.owner/<id>.json. A caller-chosen id is
// create-or-resume for both `prx -session-id` and `prx run -session`, so flow
// mints the id locally like it does for Claude Code.
package prx

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"flow/internal/harness"
	"flow/internal/spawner"
)

var (
	// SkipPermissionsRunner runs the headless close-out sweep. Tests
	// replace it to avoid invoking the CLI.
	SkipPermissionsRunner = runSkipPermissions
	// PIDAlive reports whether a process exists. Tests replace it.
	PIDAlive = pidAlive
	// Hostname is this machine's name, compared against owner records.
	Hostname = os.Hostname
)

const (
	// bypass is prx's no-approval permission mode (ask|auto|yolo). Headless
	// runs need it: with no UI, prx refuses any escalation.
	bypass = "yolo"
	// maxTurns overrides `prx run`'s default cap of 25 model/tool turns,
	// which would cut an auto run or close-out sweep off mid-task. 5000 is
	// prx's own interactive default.
	maxTurns = "5000"
)

// New returns the prx adapter.
func New() harness.Harness { return &prx{} }

type prx struct{}

func (p *prx) Name() harness.Name { return harness.NamePrx }
func (p *prx) Binary() string     { return "prx" }

// SessionIDEnvVar: prx sets PRAXIS_SESSION_ID in its bash tool's shells.
// Hook processes don't get it; they read session_id from the hook payload.
func (p *prx) SessionIDEnvVar() string { return "PRAXIS_SESSION_ID" }

// prx accepts any id without a path separator and mints UUIDv7 itself;
// flow mints v4. Both are lowercase RFC-4122 UUIDs.
var sessionIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func (p *prx) NewSessionID(workDir string) (string, error) { return newUUID() }

func (p *prx) ValidateSessionID(s string) error {
	if !sessionIDRe.MatchString(s) {
		return fmt.Errorf("not a valid lowercase prx session UUID: %q", s)
	}
	return nil
}

// ValidateSession checks the transcript exists. prx keys sessions by id,
// not cwd, so workDir is not consulted (same as Codex).
func (p *prx) ValidateSession(workDir, sessionID string) error {
	_, err := transcriptPath(sessionID)
	return err
}

func (p *prx) LaunchCmd(sessionID, prompt string, opts harness.LaunchOpts) string {
	if opts.Inject != "" {
		prompt += "\n\n" + harness.InjectionMarker + "\n" + opts.Inject
	}
	return interactive(sessionID, opts) + " -prompt " + spawner.ShellQuote(prompt)
}

// ResumeCmd passes -resume as well as -session-id: -session-id alone
// restores the model's context but the TUI doesn't redraw the conversation.
func (p *prx) ResumeCmd(sessionID string, opts harness.LaunchOpts) string {
	cmd := interactive(sessionID, opts) + " -resume " + sessionID
	if opts.Inject != "" {
		cmd += " -prompt " + spawner.ShellQuote(harness.InjectionMarker+"\n"+opts.Inject)
	}
	return cmd
}

func interactive(sessionID string, opts harness.LaunchOpts) string {
	cmd := "prx -session-id " + sessionID
	if opts.SkipPermissions {
		cmd += " -permission-mode " + bypass
	}
	// A flow-created work dir is trusted for this launch only (nothing is
	// saved), mirroring codex's per-launch override: the TUI would
	// otherwise stop at its trust prompt, which hangs a tab auto run.
	// User repos (no TrustDir) keep the prompt for a human to answer.
	if opts.TrustDir != "" {
		cmd += " -trust-workspace"
	}
	return cmd
}

func (p *prx) SkipPermissionsRun(prompt string) error { return SkipPermissionsRunner(prompt) }

// DefersCloseoutInAutoRun: prx's bash tool promotes an inline command to a
// background job after 5 minutes (tool.DefaultBashBackgroundAfter), so a
// long close-out sweep run by the auto run's own `flow done` could be cut
// off when `prx run` exits. The supervisor sweeps after exit instead (see
// harness.CloseoutDeferrer).
func (p *prx) DefersCloseoutInAutoRun() bool { return true }

func runSkipPermissions(prompt string) error {
	cmd := exec.Command("prx", "run", "-permission-mode", bypass, "-max-turns", maxTurns, "-prompt", prompt)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run()
}

// AutoRunArgv: `prx run -session` creates or resumes, so opts.Resume needs
// no flag. prx has no last-message file; its final answer is the tail of
// stdout, which the supervisor captures in the run log, so
// opts.LastMessagePath is ignored.
func (p *prx) AutoRunArgv(sessionID, prompt string, opts harness.LaunchOpts) []string {
	if opts.Inject != "" {
		prompt += "\n\n" + harness.InjectionMarker + "\n" + opts.Inject
	}
	argv := []string{"prx", "run", "-session", sessionID, "-max-turns", maxTurns}
	if opts.SkipPermissions {
		argv = append(argv, "-permission-mode", bypass)
	}
	return append(argv, "-prompt", prompt)
}

// LiveSessionIDs reads prx's owner records. A process-table scan would miss
// sessions opened from prx's dashboard or a detached host, whose argv has no
// session id. A record counts as live when its pid is alive, or when it
// belongs to another machine (prx itself treats those as live: there's no
// way to probe them from here).
func (p *prx) LiveSessionIDs() (map[string]int, error) {
	records, err := readOwnerRecords()
	if err != nil {
		return nil, err
	}
	host, _ := Hostname()
	live := map[string]int{}
	for _, o := range records {
		if o.live(host) {
			live[strings.ToLower(o.SessionID)]++
		}
	}
	return live, nil
}

// ownerRecord is the part of prx's <sessionDir>/.owner/<id>.json flow reads.
type ownerRecord struct {
	SessionID string `json:"sessionId"`
	PID       int    `json:"pid"`
	Host      string `json:"host"`
}

// readOwnerRecords returns every parseable owner record. prx writes one the
// moment a session is claimed, before its first message.
func readOwnerRecords() ([]ownerRecord, error) {
	dir, err := sessionDir()
	if err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(dir, ".owner", "*.json"))
	if err != nil {
		return nil, err
	}
	var out []ownerRecord
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var o ownerRecord
		if json.Unmarshal(raw, &o) == nil && o.SessionID != "" {
			out = append(out, o)
		}
	}
	return out, nil
}

// live reports whether the owning process is alive, or runs on another
// machine (prx itself treats those as live: there's no way to probe them).
func (o ownerRecord) live(host string) bool {
	remote := o.Host != "" && host != "" && o.Host != host
	return remote || PIDAlive(o.PID)
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// agentDir is prx's state root: $PRAXIS_CODING_AGENT_DIR, else
// ~/.praxis/agent. Skills and settings.json (hooks) live under it.
func agentDir() (string, error) {
	if d := os.Getenv("PRAXIS_CODING_AGENT_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".praxis", "agent"), nil
}

// sessionDir is $PRAXIS_CODING_AGENT_SESSION_DIR, else <agentDir>/sessions.
func sessionDir() (string, error) {
	if d := os.Getenv("PRAXIS_CODING_AGENT_SESSION_DIR"); d != "" {
		return d, nil
	}
	a, err := agentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(a, "sessions"), nil
}

func (p *prx) SkillInstallPath() (string, error) {
	dir, err := agentDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "skills", "flow", "SKILL.md"), nil
}

func (p *prx) SkillVersionPath() (string, error) {
	s, err := p.SkillInstallPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(s), "VERSION"), nil
}

func (p *prx) InstallSkill(files fs.FS) error {
	s, err := p.SkillInstallPath()
	if err != nil {
		return err
	}
	base := filepath.Dir(s)
	return fs.WalkDir(files, ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		content, err := fs.ReadFile(files, rel)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", rel, err)
		}
		target := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}
		return os.WriteFile(target, content, 0o644)
	})
}

func (p *prx) UninstallSkill() error {
	s, err := p.SkillInstallPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Dir(s)); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(filepath.Dir(s))
}

// Hooks use Claude Code's schema and output contract, which prx honors by
// default. SessionStart re-fires with source "compact" after compaction,
// where the task reload matters most. UserPromptSubmit takes no matcher
// (prx matches it against the prompt text). Tool hooks match every tool;
// the handlers exit silently when there's nothing to say.
func (p *prx) InstallSessionStartHook(command string) (bool, error) {
	return installHook("SessionStart", "startup|resume|compact", command)
}
func (p *prx) UninstallSessionStartHook(command string) (bool, error) {
	return uninstallHook("SessionStart", command)
}
func (p *prx) InstallUserPromptSubmitHook(command string) (bool, error) {
	return installHook("UserPromptSubmit", "", command)
}
func (p *prx) UninstallUserPromptSubmitHook(command string) (bool, error) {
	return uninstallHook("UserPromptSubmit", command)
}
func (p *prx) InstallPostToolUseHook(command string) (bool, error) {
	return installHook("PostToolUse", "", command)
}
func (p *prx) UninstallPostToolUseHook(command string) (bool, error) {
	return uninstallHook("PostToolUse", command)
}
func (p *prx) InstallPreToolUseHook(command string) (bool, error) {
	return installHook("PreToolUse", "", command)
}
func (p *prx) UninstallPreToolUseHook(command string) (bool, error) {
	return uninstallHook("PreToolUse", command)
}
func (p *prx) InstallStopHook(command string) (bool, error) {
	return installHook("Stop", "", command)
}
func (p *prx) UninstallStopHook(command string) (bool, error) {
	return uninstallHook("Stop", command)
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("rand read: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // v4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
