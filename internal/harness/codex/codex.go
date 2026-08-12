// Package codex implements harness.Harness for the Codex CLI.
package codex

import (
	"bufio"
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

func (c *codex) SkipPermissionsRun(prompt string) error {
	cmd := exec.Command("codex", "exec", "--dangerously-bypass-approvals-and-sandbox", prompt)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
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
		"codex", "exec", "--json", "--sandbox", "read-only",
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

func (c *codex) InstallSkill(files fs.FS) error {
	p, err := c.SkillInstallPath()
	if err != nil {
		return err
	}
	base := filepath.Dir(p)
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
		return os.WriteFile(target, content, 0o644)
	})
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
