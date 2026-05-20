// Package claude implements harness.Harness for Anthropic's Claude
// Code CLI. It pre-allocates session UUIDs (claude accepts
// --session-id), runs `claude -p` for headless sweeps, and wires
// SessionStart hooks into ~/.claude/settings.json.
package claude

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"flow/internal/harness"
	"flow/internal/spawner"
)

// Package-level seams. Tests in other packages swap these to avoid
// spawning real subprocesses.
//
//	NewUUID                — session UUID minted by NewSessionID.
//	SkipPermissionsRunner  — invocation of `claude -p` for the
//	                         close-out sweep.
//	PSRunner               — `ps -axo pid,command` output used by
//	                         LiveSessionIDs.
//
// Use t.Cleanup to restore after stubbing, exactly as iterm.Runner is
// stubbed in the existing tests.
var (
	NewUUID               = newUUID
	SkipPermissionsRunner = runSkipPermissions
	PSRunner              = runPS
)

const (
	// SessionStart matcher for ~/.claude/settings.json. Stable —
	// changing it would orphan existing installs.
	hookMatcher = "startup|resume"
)

// New returns a fresh claude harness. The struct is stateless; we
// expose it through the harness.Harness interface.
func New() harness.Harness {
	return &claude{}
}

type claude struct{}

// ---------- identity ----------

func (c *claude) Name() harness.Name      { return harness.NameClaude }
func (c *claude) Binary() string          { return "claude" }
func (c *claude) SessionIDEnvVar() string { return "CLAUDE_CODE_SESSION_ID" }

// ---------- session allocation ----------

// sessionIDRe mirrors `claude --session-id`'s contract: standard v4
// UUID, lowercase hex, with version/variant bits enforced.
var sessionIDRe = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

// NewSessionID generates a v4 UUID locally. flow's caller writes it
// to tasks.session_id before spawning so `claude --session-id <uuid>`
// produces a transcript at a deterministic path. (Codex/Gemini will
// implement this by probing the harness CLI to mint and capture an
// id; claude doesn't need that — it accepts an externally-supplied
// UUID via --session-id.)
func (c *claude) NewSessionID() (string, error) {
	return NewUUID()
}

func (c *claude) ValidateSessionID(s string) error {
	if !sessionIDRe.MatchString(s) {
		return fmt.Errorf("not a valid claude v4 UUID: %q", s)
	}
	return nil
}

// newUUID generates a v4 UUID in the 8-4-4-4-12 hex format that
// `claude --session-id` accepts.
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

// LaunchCmd builds `claude --session-id <uuid> <quoted-prompt> [--dangerously-skip-permissions]`.
// Injection text (opts.Inject) is appended to the prompt with the
// shared harness.InjectionMarker so the receiving session can
// distinguish it from typed user input.
func (c *claude) LaunchCmd(sessionID, prompt string, opts harness.LaunchOpts) string {
	if opts.Inject != "" {
		prompt = prompt + "\n\n" + harness.InjectionMarker + "\n" + opts.Inject
	}
	cmd := fmt.Sprintf("claude --session-id %s %s", sessionID, spawner.ShellQuote(prompt))
	if opts.SkipPermissions {
		cmd += " --dangerously-skip-permissions"
	}
	return cmd
}

// ResumeCmd builds `claude --resume <uuid> [<quoted-injection>] [--dangerously-skip-permissions]`.
func (c *claude) ResumeCmd(sessionID string, opts harness.LaunchOpts) string {
	cmd := "claude --resume " + sessionID
	if opts.Inject != "" {
		cmd += " " + spawner.ShellQuote(harness.InjectionMarker+"\n"+opts.Inject)
	}
	if opts.SkipPermissions {
		cmd += " --dangerously-skip-permissions"
	}
	return cmd
}

// ---------- headless ----------

func (c *claude) SkipPermissionsRun(prompt string) error {
	return SkipPermissionsRunner(prompt)
}

// runSkipPermissions is the default SkipPermissionsRunner — execs
// `claude -p <prompt> --dangerously-skip-permissions`. Stdout/stderr
// are discarded because the sweep prompt instructs claude to write
// files silently with no chat output.
func runSkipPermissions(prompt string) error {
	cmd := exec.Command("claude", "-p", prompt, "--dangerously-skip-permissions")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// ---------- live-session detection ----------

// runningArgRe matches `--session-id <uuid>` or `--resume <uuid>` in a
// process command line. Uppercase-tolerant for paranoia (some tools
// normalize differently).
var runningArgRe = regexp.MustCompile(
	`(?:--session-id|--resume)[ =]([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12})`,
)

// LiveSessionIDs scans the process table for claude invocations
// carrying --session-id or --resume and returns counts per UUID
// (lowercased). A count > 1 means the same session is being driven
// by multiple processes — both write to the same jsonl and can race;
// `flow do` warns the user when this happens.
//
// Bare `claude` invocations without a UUID flag are not detectable.
// `flow do --here` is the supported path for binding such sessions.
func (c *claude) LiveSessionIDs() (map[string]int, error) {
	out, err := PSRunner()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	live := make(map[string]int)
	for _, line := range strings.Split(string(out), "\n") {
		// Heuristic: the row must mention claude. Bare `claude` and
		// fully-qualified paths like `/Users/rohit/.bun/bin/claude`
		// both appear in practice. We match the literal token
		// "claude" to avoid catching unrelated processes.
		if !strings.Contains(line, "claude") {
			continue
		}
		// Each row counts once even if argv mentions a UUID twice
		// (some shells echo the command). Dedupe per row.
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
//
// RenderTranscript and the jsonl decoder it uses live in transcript.go
// in this package.

// EncodeCwd encodes an absolute cwd path for Claude Code's
// ~/.claude/projects/<dir> directory naming. Empirically: the
// characters `/`, `.`, and `_` are each replaced by `-`. Other
// characters pass through unchanged.
//
// Samples:
//
//	/Users/alice/code/myapp                      → -Users-alice-code-myapp
//	/Users/alice/.flow/tasks/foo/workspace       → -Users-alice--flow-tasks-foo-workspace
//	/Users/alice/.cache/work/_default            → -Users-alice--cache-work--default
//
// If CC introduces a new substitution in a future version, add the
// char here and add a sample case to claude_test.go.
//
// Exported because some test code in the app package pre-creates
// fake transcript files at the encoded path; internal callers
// (RenderTranscript) use this same function.
func EncodeCwd(cwd string) string {
	r := strings.NewReplacer("/", "-", ".", "-", "_", "-")
	return r.Replace(cwd)
}

// ---------- skill install ----------

// SkillInstallPath returns ~/.claude/skills/flow/SKILL.md.
func (c *claude) SkillInstallPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "skills", "flow", "SKILL.md"), nil
}

// SkillVersionPath returns the sidecar VERSION file alongside SKILL.md.
func (c *claude) SkillVersionPath() (string, error) {
	skill, err := c.SkillInstallPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(skill), "VERSION"), nil
}

// InstallSkill writes content to SkillInstallPath. Creates parent dirs.
func (c *claude) InstallSkill(content []byte) error {
	p, err := c.SkillInstallPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	return nil
}

// UninstallSkill removes the skill directory entirely (SKILL.md plus
// VERSION sidecar plus anything else dropped alongside).
func (c *claude) UninstallSkill() error {
	p, err := c.SkillInstallPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(dir)
}

// ---------- hooks ----------

// settingsPath returns ~/.claude/settings.json.
func settingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

func (c *claude) InstallSessionStartHook(command string) (bool, error) {
	return installHook("SessionStart", hookMatcher, command)
}

func (c *claude) UninstallSessionStartHook(command string) (bool, error) {
	return uninstallHook("SessionStart", command)
}

func (c *claude) UninstallUserPromptSubmitHook(command string) (bool, error) {
	return uninstallHook("UserPromptSubmit", command)
}

// installHook idempotently adds a hook entry for `event` to
// ~/.claude/settings.json. matcher may be empty — some events don't
// use one and the field is omitted. command is both the literal
// command Claude Code will execute AND the marker used to detect
// whether the hook is already installed.
//
// Returns (added=true) iff the file was actually modified. Preserves
// all other settings, all other events, and all sibling entries
// under the same event.
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

// uninstallHook removes any entry under hooks.<event> whose inner
// hook list contains a command matching `command`. Returns
// (removed=true) iff the file changed.
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
