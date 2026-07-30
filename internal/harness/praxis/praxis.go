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
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"flow/internal/harness"
	"flow/internal/harness/shellquote"
)

// Package-level seams (mirrors the claude adapter). Tests swap these
// to avoid spawning real subprocesses.
var (
	NewUUID               = newUUID
	SkipPermissionsRunner = runSkipPermissions
	PSRunner              = runPS
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

// ---------- session allocation ----------

// sessionIDRe accepts any valid UUID (v1–v7), not just v4. Praxis uses
// UUIDv7 (time-sortable) for its session IDs (session.NewID → uuid.NewV7),
// so the claude adapter's strict v4-only regex would reject them. We
// validate the structural format (8-4-4-4-12 hex) and the standard
// version (1-7) / variant (8-b) nibbles, but don't pin to v4.
var sessionIDRe = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[1-7][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

// NewSessionID generates a v4 UUID locally. flow's caller writes it to
// tasks.session_id before spawning so `praxis chat --session-id <uuid>`
// creates a session at a deterministic id. (We use v4 rather than v7
// here because flow's DB and other harnesses expect v4; praxis's
// --session-id accepts any valid UUID string, so a v4 id is fine.)
func (p *praxis) NewSessionID() (string, error) {
	return NewUUID()
}

func (p *praxis) ValidateSessionID(s string) error {
	if !sessionIDRe.MatchString(s) {
		return fmt.Errorf("not a valid praxis UUID: %q", s)
	}
	return nil
}

// ValidateSession verifies that a session transcript exists at the
// praxis session path. Praxis stores sessions as
// ~/.praxis/agent/sessions/<sessionID>/session.jsonl (or legacy
// ~/.praxis/agent/sessions/<sessionID>.jsonl). Unlike claude, the
// path is keyed by session id alone, NOT by cwd — so we just check
// both layouts.
var StatFn = func(path string) error {
	_, err := os.Stat(path)
	return err
}

func (p *praxis) ValidateSession(workDir, sessionID string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("read home dir: %w", err)
	}
	sessionsDir := filepath.Join(home, ".praxis", "agent", "sessions")

	// Current layout: <sessions>/<id>/session.jsonl
	nested := filepath.Join(sessionsDir, sessionID, "session.jsonl")
	if err := StatFn(nested); err == nil {
		return nil
	}

	// Legacy flat layout: <sessions>/<id>.jsonl
	flat := filepath.Join(sessionsDir, sessionID+".jsonl")
	if err := StatFn(flat); err == nil {
		return nil
	}

	return fmt.Errorf("praxis session transcript not found at %s or %s", nested, flat)
}

// newUUID generates a v4 UUID in the 8-4-4-4-12 hex format.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("rand read: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // v4
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

// AutoRunArgv builds `praxis run --prompt <prompt> --session <uuid>` as
// argv for the `flow do --auto` headless supervisor. `praxis run` is
// non-interactive by definition; the PR #65 CLI exposes no separate
// permission-mode flag for it. The caller’s shell environment must enable
// PRAXIS_EXPERIMENTAL. --session pins the session id so a transcript exists
// for the run's own close-out sweep and `flow transcript`.
func (p *praxis) AutoRunArgv(sessionID, prompt string, opts harness.LaunchOpts) []string {
	if opts.Inject != "" {
		prompt = prompt + "\n\n" + harness.InjectionMarker + "\n" + opts.Inject
	}
	return []string{"praxis", "run", "--session", sessionID, "--prompt", prompt}
}

// runSkipPermissions is the default SkipPermissionsRunner — execs
// `praxis run --prompt <prompt>`. Stdout/stderr are discarded. The
// caller’s environment, not Flow, controls PRAXIS_EXPERIMENTAL.
func runSkipPermissions(prompt string) error {
	cmd := exec.Command("praxis", "run", "--prompt", prompt)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
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

// RenderTranscript reads praxis's on-disk session transcript (JSONL at
// ~/.praxis/agent/sessions/<id>/session.jsonl or the legacy flat layout)
// and writes a normalized human-readable rendering to w.
//
// Praxis's session.jsonl format differs from claude's: each line is a
// JSON object with a "type" field ("session" header, "message" entries,
// "summary"/"compaction" markers, etc.) and message entries carry an
// embedded "message" object with role/content. We normalize to the same
// text shape as the claude transcript renderer.
func (p *praxis) RenderTranscript(cwd, sessionID string, compact bool, w io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("no home dir: %w", err)
	}

	path, err := resolveTranscriptPath(home, sessionID)
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

// resolveTranscriptPath locates a praxis session transcript. Tries the
// current nested layout first, then legacy flat.
func resolveTranscriptPath(home, sessionID string) (string, error) {
	sessionsDir := filepath.Join(home, ".praxis", "agent", "sessions")

	nested := filepath.Join(sessionsDir, sessionID, "session.jsonl")
	if _, err := os.Stat(nested); err == nil {
		return nested, nil
	}

	flat := filepath.Join(sessionsDir, sessionID+".jsonl")
	if _, err := os.Stat(flat); err == nil {
		return flat, nil
	}

	return "", fmt.Errorf(
		"praxis transcript not found: no session.jsonl at %s or %s",
		nested, flat,
	)
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

func (p *praxis) InstallSessionStartHook(command string) (bool, error) {
	return installHook("SessionStart", hookMatcher, command)
}

func (p *praxis) UninstallSessionStartHook(command string) (bool, error) {
	return uninstallHook("SessionStart", command)
}

func (p *praxis) InstallUserPromptSubmitHook(command string) (bool, error) {
	return installHook("UserPromptSubmit", "", command)
}

func (p *praxis) UninstallUserPromptSubmitHook(command string) (bool, error) {
	return uninstallHook("UserPromptSubmit", command)
}

// installHook and uninstallHook are identical to the claude adapter's
// implementations — praxis uses a Claude-compatible settings.json hook
// format. The only difference is the settings file path (settingsPath
// above resolves to ~/.praxis/agent/settings.json instead of
// ~/.claude/settings.json).

func installHook(event, matcher, command string) (bool, error) {
	path, err := settingsPath()
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("read %s: %w", path, err)
		}
		raw = []byte("{}")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
		}
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	if settings == nil {
		settings = map[string]any{}
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	entries, _ := hooks[event].([]any)

	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		inner, _ := m["hooks"].([]any)
		for _, h := range inner {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmd, _ := hm["command"].(string); cmd == command {
				return false, nil
			}
		}
	}

	newEntry := map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": command,
			},
		},
	}
	if matcher != "" {
		newEntry["matcher"] = matcher
	}
	entries = append(entries, newEntry)
	hooks[event] = entries
	settings["hooks"] = hooks

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal settings: %w", err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

func uninstallHook(event, command string) (bool, error) {
	path, err := settingsPath()
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return false, nil
	}
	entries, _ := hooks[event].([]any)
	if len(entries) == 0 {
		return false, nil
	}

	changed := false
	kept := make([]any, 0, len(entries))
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			kept = append(kept, entry)
			continue
		}
		inner, _ := m["hooks"].([]any)
		filteredInner := make([]any, 0, len(inner))
		for _, h := range inner {
			hm, ok := h.(map[string]any)
			if !ok {
				filteredInner = append(filteredInner, h)
				continue
			}
			cmd, _ := hm["command"].(string)
			if strings.TrimSpace(cmd) == command {
				changed = true
				continue
			}
			filteredInner = append(filteredInner, h)
		}
		if len(filteredInner) == 0 {
			changed = true
			continue
		}
		m["hooks"] = filteredInner
		kept = append(kept, m)
	}

	if !changed {
		return false, nil
	}
	if len(kept) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = kept
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	} else {
		settings["hooks"] = hooks
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal settings: %w", err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}
