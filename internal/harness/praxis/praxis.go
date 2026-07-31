// Package praxis implements harness.Harness for the Praxis coding agent
// (praxis-cli's `praxis chat` command, which embeds the praxis-harness
// TUI/SDK in-process). It pre-allocates session UUIDs (praxis accepts
// --session-id), builds `praxis chat` launch/resume commands, and wires
// SessionStart / UserPromptSubmit hooks into ~/.praxis/agent/settings.json.
//
// praxis exports PRAXIS_SESSION_ID into child processes (analogous to
// CLAUDE_CODE_SESSION_ID), which is what flow's hook reverse-looks-up
// against tasks.session_id to find the bound task.
//
// IMPORTANT: `praxis chat` is gated behind PRAXIS_EXPERIMENTAL=1. Flow
// never sets that variable and never appends --experimental; the user is
// responsible for exporting it in their shell profile before using this
// adapter. This keeps Flow from changing the user’s Praxis feature policy.
package praxis

import (
	"crypto/rand"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"flow/internal/harness"
	"flow/internal/harness/hooksettings"
	"flow/internal/shellquote"
)

// Package-level seams (mirrors the claude adapter). Tests swap these
// to avoid spawning real subprocesses.
var (
	NewUUID               = newUUID
	SkipPermissionsRunner = runSkipPermissions
	PSRunner              = runPS
	PreflightRunner       = probeChatSubcommand
	LookPathFn            = exec.LookPath
)

const (
	// SessionStart matcher for ~/.praxis/agent/settings.json. Uses the
	// same "startup|resume" value as claude's settings.json — praxis's
	// hook system is Claude-compatible.
	hookMatcher = "startup|resume"
)

// New returns a fresh praxis harness. The struct is stateless.
func New() harness.Harness {
	return &praxis{}
}

type praxis struct{}

// ---------- identity ----------

func (p *praxis) Name() harness.Name      { return harness.NamePraxis }
func (p *praxis) Binary() string          { return "praxis" }
func (p *praxis) SessionIDEnvVar() string { return "PRAXIS_SESSION_ID" }

// Preflight checks that `praxis` is on PATH and that this build actually
// ships the `chat` subcommand every launch/resume path depends on. The
// second half is not paranoia: the released praxis CLI predates `chat`,
// and a `praxis` on PATH that lacks it exits instantly inside the freshly
// spawned tab, which flow cannot observe.
//
// `praxis chat --help` is the probe. It is registered unconditionally, so
// --help succeeds regardless of PRAXIS_EXPERIMENTAL and does not start a
// session; a build without the subcommand fails with cobra's "unknown
// command".
func (p *praxis) Preflight() error {
	if _, err := LookPathFn("praxis"); err != nil {
		return fmt.Errorf("praxis CLI not found on PATH: %w", err)
	}
	if err := PreflightRunner(); err != nil {
		return fmt.Errorf("this praxis build has no `chat` subcommand "+
			"(need a CLI that ships `praxis chat`): %w", err)
	}
	return nil
}

// probeChatSubcommand is the default PreflightRunner.
func probeChatSubcommand() error {
	cmd := exec.Command("praxis", "chat", "--help")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// ---------- session allocation ----------

// sessionIDRe accepts any valid UUID (v1–v7), not just v4. Praxis uses
// UUIDv7 (time-sortable) for its session IDs (session.NewID → uuid.NewV7),
// so the claude adapter's strict v4-only regex would reject them. We
// validate the structural format (8-4-4-4-12 hex) and the standard
// version (1-7) / variant (8-b) nibbles, but don't pin to v4.
var sessionIDRe = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[1-7][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

// NewSessionID generates a UUIDv7 locally. flow's caller writes it to
// tasks.session_id before spawning so `praxis chat --session-id <uuid>`
// creates a session at a deterministic id.
//
// v7 specifically, because praxis mints its own session ids with
// uuid.NewV7 and its session store is a flat directory keyed by id — the
// leading millisecond timestamp is what makes that directory sort
// chronologically. A v4 id from flow would be a random name wedged among
// time-ordered ones, so flow-created sessions would scatter in praxis's
// own session listing. Nothing in flow constrains the version:
// tasks.session_id is TEXT and ValidateSessionID accepts v1-v7.
func (p *praxis) NewSessionID() (string, error) {
	return NewUUID()
}

func (p *praxis) ValidateSessionID(s string) error {
	if !sessionIDRe.MatchString(s) {
		return fmt.Errorf("not a valid praxis UUID: %q", s)
	}
	return nil
}

// StatFn is the existence check used when locating a transcript. Tests
// swap it to avoid touching the real filesystem (mirrors claude.StatFn).
var StatFn = func(path string) error {
	_, err := os.Stat(path)
	return err
}

// ValidateSession verifies that a transcript exists for sessionID.
//
// workDir is ignored: unlike claude, praxis keys its session store by
// session id alone, so there is no cwd for a transcript to disagree with
// and nothing here can drift from the task's work_dir.
func (p *praxis) ValidateSession(workDir, sessionID string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("read home dir: %w", err)
	}
	_, err = findTranscript(home, sessionID)
	return err
}

// findTranscript locates the session.jsonl for sessionID, returning its
// path. Two layouts exist:
//
//	current: <sessions>/<id>/session.jsonl
//	legacy:  <sessions>/<encoded-cwd>/<timestamp>Z_<id>.jsonl
//
// The legacy layout keyed the directory by cwd (claude-style) and prefixed
// the file with a start timestamp, so it cannot be reconstructed from the
// session id alone — hence the glob. The deterministic current-layout
// check runs first, so the glob only costs a directory scan for sessions
// old enough to predate the migration.
func findTranscript(home, sessionID string) (string, error) {
	sessionsDir := filepath.Join(home, ".praxis", "agent", "sessions")

	nested := filepath.Join(sessionsDir, sessionID, "session.jsonl")
	if err := StatFn(nested); err == nil {
		return nested, nil
	}

	legacyGlob := filepath.Join(sessionsDir, "*", "*_"+sessionID+".jsonl")
	matches, _ := filepath.Glob(legacyGlob)
	// The same id can appear under two cwd-encoded dirs when a session was
	// resumed from elsewhere; prefer the most recently written.
	newest, newestMod := "", int64(-1)
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		if mod := fi.ModTime().UnixNano(); mod > newestMod {
			newest, newestMod = m, mod
		}
	}
	if newest != "" {
		return newest, nil
	}

	return "", fmt.Errorf("praxis session transcript not found at %s or %s", nested, legacyGlob)
}

// newUUID generates a UUIDv7 (RFC 9562) in the 8-4-4-4-12 hex format:
// a 48-bit big-endian unix-millisecond prefix, then the version and
// variant nibbles, then random bits. Ids minted in the same millisecond
// still differ in their 74 random bits.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("rand read: %w", err)
	}
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// ---------- launching ----------

// LaunchCmd builds `praxis chat --session-id <uuid> --prompt <quoted-prompt>`.
// `praxis chat` only reads an initial message from --prompt; positional
// arguments are not a prompt channel. The user must export
// PRAXIS_EXPERIMENTAL=1 in their shell profile; Flow deliberately does not
// set that variable or append --experimental.
func (p *praxis) LaunchCmd(sessionID, prompt string, opts harness.LaunchOpts) string {
	if opts.Inject != "" {
		prompt = prompt + "\n\n" + harness.InjectionMarker + "\n" + opts.Inject
	}
	cmd := fmt.Sprintf("praxis chat --session-id %s --prompt %s", sessionID, shellquote.Quote(prompt))
	if opts.SkipPermissions {
		cmd += " --permission-mode yolo"
	}
	return cmd
}

// ResumeCmd builds `praxis chat --session-id <uuid>`. `--session-id` is
// the create-or-resume UUID contract; Praxis's --resume flag instead takes
// a persisted session-file path. The caller's shell environment must enable
// PRAXIS_EXPERIMENTAL.
func (p *praxis) ResumeCmd(sessionID string, opts harness.LaunchOpts) string {
	cmd := "praxis chat --session-id " + sessionID
	if opts.Inject != "" {
		cmd += " --prompt " + shellquote.Quote(harness.InjectionMarker+"\n"+opts.Inject)
	}
	if opts.SkipPermissions {
		cmd += " --permission-mode yolo"
	}
	return cmd
}

// ---------- headless ----------

func (p *praxis) SkipPermissionsRun(prompt string) error {
	return SkipPermissionsRunner(prompt)
}

// autoRunMaxTurns is the per-run turn budget for headless `flow do --auto`
// runs. It must be set explicitly: `praxis run` defaults --max-turns to 25,
// and the SDK substitutes the same 25 for any value <= 0, so both the flag
// default and an explicit 0 would truncate a real autonomous run long
// before it reaches its own `flow done` close-out. 1000 matches what the
// Praxis TUI uses for an interactive session — a runaway-loop backstop
// rather than a working limit. Claude's headless `-p` has no cap at all.
const autoRunMaxTurns = "1000"

// AutoRunArgv builds `praxis run --prompt <prompt> --session <uuid>` as
// argv for the `flow do --auto` headless supervisor. `praxis run` is
// non-interactive by definition and exposes no permission-mode flag. The
// caller’s shell environment must enable PRAXIS_EXPERIMENTAL. --session
// pins the session id so a transcript exists for the run's own close-out
// sweep and `flow transcript`.
func (p *praxis) AutoRunArgv(sessionID, prompt string, opts harness.LaunchOpts) []string {
	if opts.Inject != "" {
		prompt = prompt + "\n\n" + harness.InjectionMarker + "\n" + opts.Inject
	}
	return []string{
		"praxis", "run",
		"--session", sessionID,
		"--max-turns", autoRunMaxTurns,
		"--prompt", prompt,
	}
}

// runSkipPermissions is the default SkipPermissionsRunner — execs
// `praxis run --prompt <prompt>`. Stdout is discarded but stderr is
// captured into the returned error: these sweeps run unattended, and the
// most likely failure (PRAXIS_EXPERIMENTAL not exported, so `praxis run`
// refuses) is only diagnosable from praxis's own message. The caller’s
// environment, not Flow, controls PRAXIS_EXPERIMENTAL.
func runSkipPermissions(prompt string) error {
	var stderr strings.Builder
	cmd := exec.Command("praxis", "run", "--max-turns", autoRunMaxTurns, "--prompt", prompt)
	cmd.Stdout = io.Discard
	cmd.Stderr = &limitedWriter{w: &stderr, remaining: maxCapturedStderr}
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// maxCapturedStderr bounds how much of a failing run's stderr is folded
// into the error message.
const maxCapturedStderr = 2048

// limitedWriter forwards at most `remaining` bytes and silently drops the
// rest, so a chatty subprocess cannot balloon an error string.
type limitedWriter struct {
	w         io.Writer
	remaining int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.remaining <= 0 {
		return len(p), nil
	}
	keep := p
	if len(keep) > l.remaining {
		keep = keep[:l.remaining]
	}
	n, err := l.w.Write(keep)
	l.remaining -= n
	if err != nil {
		return n, err
	}
	// Report the full length: the caller wrote everything it intended and
	// a short count would surface as io.ErrShortWrite.
	return len(p), nil
}

// ---------- live-session detection ----------

// runningArgRe matches `--session-id <uuid>` (interactive) or
// `--session <uuid>` (headless) in a process command line.
var runningArgRe = regexp.MustCompile(
	`(?:--session-id|--session)[ =]([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-7][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12})`,
)

// LiveSessionIDs scans the process table for praxis invocations
// carrying --session-id or --session and returns counts per
// UUID (lowercased).
func (p *praxis) LiveSessionIDs() (map[string]int, error) {
	out, err := PSRunner()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	live := make(map[string]int)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "praxis") {
			continue
		}
		seen := map[string]bool{}
		for _, m := range runningArgRe.FindAllStringSubmatch(line, -1) {
			if len(m) < 2 {
				continue
			}
			id := strings.ToLower(m[1])
			if seen[id] {
				continue
			}
			seen[id] = true
			live[id]++
		}
	}
	return live, nil
}

func runPS() ([]byte, error) {
	return exec.Command("ps", "-axo", "pid,command").Output()
}

// ---------- transcript ----------

// RenderTranscript reads praxis's on-disk session transcript and writes a
// normalized human-readable rendering to w. cwd is unused — praxis keys
// its session store by id, so findTranscript needs nothing else.
//
// Praxis's session.jsonl format differs from claude's: each line is a
// JSON object with a "type" field ("session" header, "message" entries,
// "todo_update" and other bookkeeping) and message entries carry an
// embedded "message" object with role/content. renderJSONL normalizes it
// into the same section vocabulary the claude renderer emits.
func (p *praxis) RenderTranscript(cwd, sessionID string, compact bool, w io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("no home dir: %w", err)
	}

	path, err := findTranscript(home, sessionID)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open praxis transcript %s: %w", path, err)
	}
	defer f.Close()
	return renderJSONL(f, compact, w)
}

// ---------- skill install ----------

// SkillInstallPath returns ~/.praxis/agent/skills/flow/SKILL.md.
func (p *praxis) SkillInstallPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".praxis", "agent", "skills", "flow", "SKILL.md"), nil
}

// SkillVersionPath returns the sidecar VERSION file alongside SKILL.md.
func (p *praxis) SkillVersionPath() (string, error) {
	skill, err := p.SkillInstallPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(skill), "VERSION"), nil
}

// InstallSkill writes the skill tree into the directory that holds
// SkillInstallPath, preserving each file's relative path.
func (p *praxis) InstallSkill(files fs.FS) error {
	skillPath, err := p.SkillInstallPath()
	if err != nil {
		return err
	}
	base := filepath.Dir(skillPath)
	return fs.WalkDir(files, ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		content, err := fs.ReadFile(files, rel)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", rel, err)
		}
		target := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
		return nil
	})
}

// UninstallSkill removes the skill directory entirely.
func (p *praxis) UninstallSkill() error {
	skillPath, err := p.SkillInstallPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(skillPath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(dir)
}

// ---------- hooks ----------

// settingsPath returns ~/.praxis/agent/settings.json.
func settingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".praxis", "agent", "settings.json"), nil
}

// Praxis uses a Claude-compatible settings.json hook format — its loader
// compiles `matcher` as a regexp and executes `command` entries the same
// way — so the mutation logic is shared via harness/hooksettings and only
// the file path differs from the claude adapter.

func (p *praxis) InstallSessionStartHook(command string) (bool, error) {
	path, err := settingsPath()
	if err != nil {
		return false, err
	}
	return hooksettings.Install(path, "SessionStart", hookMatcher, command)
}

func (p *praxis) UninstallSessionStartHook(command string) (bool, error) {
	path, err := settingsPath()
	if err != nil {
		return false, err
	}
	return hooksettings.Uninstall(path, "SessionStart", command)
}

func (p *praxis) InstallUserPromptSubmitHook(command string) (bool, error) {
	path, err := settingsPath()
	if err != nil {
		return false, err
	}
	// UserPromptSubmit takes no matcher — the event fires on every prompt.
	return hooksettings.Install(path, "UserPromptSubmit", "", command)
}

func (p *praxis) UninstallUserPromptSubmitHook(command string) (bool, error) {
	path, err := settingsPath()
	if err != nil {
		return false, err
	}
	return hooksettings.Uninstall(path, "UserPromptSubmit", command)
}
