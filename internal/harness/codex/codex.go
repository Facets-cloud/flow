// Package codex implements harness.Harness for the Codex CLI.
package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"flow/internal/harness"
	"flow/internal/spawner"
)

var (
	// ProbeRunner mints a persisted Codex thread. Tests replace it to avoid
	// invoking the CLI.
	ProbeRunner = runProbe
	PSRunner    = runPS
)

func New() harness.Harness { return &codex{} }

type codex struct{}

func (c *codex) Name() harness.Name      { return harness.NameCodex }
func (c *codex) Binary() string          { return "codex" }
func (c *codex) SessionIDEnvVar() string { return "CODEX_THREAD_ID" }

// ---------- capabilities ----------

// Vocab is codex's dialect. AskTool is deliberately empty: codex has no
// interactive multiple-choice tool, so flow's prompts fall back to prose
// instead of naming a tool that does not exist.
func (c *codex) Vocab() harness.Vocabulary {
	return harness.Vocabulary{
		Product:     "Codex",
		ContextFile: "AGENTS.md",
		AskTool:     "",
		SkillHint:   "by reading the flow skill under ~/.codex/skills/flow",
	}
}

func (c *codex) Resume() harness.Resumer              { return c }
func (c *codex) Headless() harness.HeadlessRunner     { return c }
func (c *codex) Transcript() harness.TranscriptSource { return c }
func (c *codex) Skills() harness.SkillInstaller       { return c }
func (c *codex) Hooks() harness.HookWirer             { return c }

// Background is nil: codex exposes no detached-agent registry of its
// own, so `$FLOW_TERM=bg` falls back to flow's own --auto supervisor
// rather than pretending an Agent View exists.
func (c *codex) Background() harness.BackgroundLauncher { return nil }

// Compile-time proof that the receiver really satisfies every
// capability it hands out — so a dropped method surfaces here rather
// than as a nil accessor at runtime.
var (
	_ harness.Harness          = (*codex)(nil)
	_ harness.Resumer          = (*codex)(nil)
	_ harness.HeadlessRunner   = (*codex)(nil)
	_ harness.TranscriptSource = (*codex)(nil)
	_ harness.SkillInstaller   = (*codex)(nil)
	_ harness.HookWirer        = (*codex)(nil)
)

// Codex thread ids are lowercase UUIDs. Codex currently creates UUIDv7
// threads, while older saved threads may use other RFC-4122 versions.
var sessionIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func (c *codex) NewSessionID() (string, error) {
	out, err := ProbeRunner()
	if err != nil {
		return "", fmt.Errorf("mint Codex session: %w", err)
	}
	return parseThreadID(out)
}

func (c *codex) ValidateSessionID(s string) error {
	if !sessionIDRe.MatchString(s) {
		return fmt.Errorf("not a valid lowercase Codex UUID: %q", s)
	}
	return nil
}

// Codex stores transcripts by globally unique thread id, rather than cwd.
func (c *codex) ValidateSession(workDir, sessionID string) error { return nil }

func (c *codex) LaunchCmd(sessionID, prompt string, opts harness.LaunchOpts) string {
	if opts.Inject != "" {
		prompt += "\n\n" + harness.InjectionMarker + "\n" + opts.Inject
	}
	cmd := "codex"
	if opts.SkipPermissions {
		cmd += " --dangerously-bypass-approvals-and-sandbox"
	}
	return cmd + " resume " + sessionID + " " + spawner.ShellQuote(prompt)
}

func (c *codex) ResumeCmd(sessionID string, opts harness.LaunchOpts) string {
	cmd := "codex"
	if opts.SkipPermissions {
		cmd += " --dangerously-bypass-approvals-and-sandbox"
	}
	cmd += " resume " + sessionID
	if opts.Inject != "" {
		cmd += " " + spawner.ShellQuote(harness.InjectionMarker+"\n"+opts.Inject)
	}
	return cmd
}

// SkipPermissionsRun runs the close-out sweep. Stdout is discarded (the
// sweep prompt asks for silent file writes), but stderr is kept and
// folded into the error: a bare exit code is not a diagnosis, and this
// path has no other observer.
func (c *codex) SkipPermissionsRun(prompt string, opts harness.LaunchOpts) error {
	cmd := exec.Command("codex", "exec", "--dangerously-bypass-approvals-and-sandbox", prompt)
	cmd.Dir = opts.WorkDir
	return harness.RunCapturingStderr(cmd)
}

func (c *codex) AutoRunArgv(sessionID, prompt string, opts harness.LaunchOpts) []string {
	if opts.Inject != "" {
		prompt += "\n\n" + harness.InjectionMarker + "\n" + opts.Inject
	}
	argv := []string{"codex", "exec", "resume"}
	if opts.SkipPermissions {
		argv = append(argv, "--dangerously-bypass-approvals-and-sandbox")
	}
	return append(argv, sessionID, prompt)
}

func runProbe() ([]byte, error) {
	// This probe intentionally persists its thread: the interactive `codex
	// resume` started immediately afterwards must resume this exact id.
	return exec.Command(
		"codex", "exec", "--json", "--sandbox", "read-only", "--skip-git-repo-check",
		"Reply exactly with OK. Do not make any changes.",
	).Output()
}

func parseThreadID(out []byte) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		var event struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Type == "thread.started" {
			if err := New().ValidateSessionID(event.ThreadID); err != nil {
				return "", fmt.Errorf("Codex probe returned invalid thread id: %w", err)
			}
			return event.ThreadID, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read Codex probe output: %w", err)
	}
	return "", fmt.Errorf("Codex probe output did not include a thread.started event")
}

var runningSessionRe = regexp.MustCompile(`\bresume\s+([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\b`)

func (c *codex) LiveSessionIDs() (map[string]int, error) {
	out, err := PSRunner()
	if err != nil {
		return nil, err
	}
	live := map[string]int{}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(strings.ToLower(line), "codex") {
			continue
		}
		seen := map[string]bool{}
		for _, m := range runningSessionRe.FindAllStringSubmatch(line, -1) {
			id := strings.ToLower(m[1])
			if !seen[id] {
				live[id]++
				seen[id] = true
			}
		}
	}
	return live, nil
}

func runPS() ([]byte, error) { return exec.Command("ps", "-axo", "pid,command").Output() }

func (c *codex) SkillInstallPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".codex", "skills", "flow", "SKILL.md"), nil
}

func (c *codex) SkillVersionPath() (string, error) {
	p, err := c.SkillInstallPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "VERSION"), nil
}

// OwnsSkillDir is true: ~/.codex/skills/flow is flow's own tree under
// codex's home, so uninstalling really does delete it.
func (c *codex) OwnsSkillDir() bool { return true }

// InstallSkill makes the skill directory equal the embedded tree — same
// pruning contract as every other harness, so a renamed reference does
// not leave an orphan behind forever.
func (c *codex) InstallSkill(files fs.FS) error {
	p, err := c.SkillInstallPath()
	if err != nil {
		return err
	}
	return harness.SyncTree(files, filepath.Dir(p), harness.SkillVersionFile)
}

func (c *codex) UninstallSkill() error {
	p, err := c.SkillInstallPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Dir(p)); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(filepath.Dir(p))
}

// PreparePrompt is a no-op: codex receives flow's SessionStart context
// through its own hooks.json, not through the prompt.
func (c *codex) PreparePrompt(prompt, _ string) string { return prompt }

// Strategies: codex has a real lifecycle-event system, so flow's
// context is delivered by patching ~/.codex/hooks.json.
func (c *codex) Strategies() []string { return []string{harness.StrategyConfigPatch} }

func (c *codex) InstallSessionStartHook(command string) (bool, error) {
	return installHook("SessionStart", "startup|resume", command)
}

func (c *codex) UninstallSessionStartHook(command string) (bool, error) {
	return uninstallHook("SessionStart", command)
}

// Codex currently exposes SessionStart but no per-prompt equivalent to
// Claude's UserPromptSubmit event. Keep the interface operation a no-op.
func (c *codex) InstallUserPromptSubmitHook(command string) (bool, error)   { return false, nil }
func (c *codex) UninstallUserPromptSubmitHook(command string) (bool, error) { return false, nil }
