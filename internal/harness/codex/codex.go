// Package codex implements harness.Harness for OpenAI Codex CLI.
//
// Codex owns its session identifiers: flow mints one by running a
// short non-interactive `codex exec --json` probe, stores the reported
// id on the task, then starts the interactive tab by resuming that id.
package codex

import (
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

var (
	ExecRunner            = runCodexExec
	SkipPermissionsRunner = runSkipPermissions
	PSRunner              = runPS
)

const hookMatcher = "startup|resume"

func New() harness.Harness {
	return &codex{}
}

type codex struct{}

func (c *codex) Name() harness.Name      { return harness.NameCodex }
func (c *codex) Binary() string          { return "codex" }
func (c *codex) SessionIDEnvVar() string { return "CODEX_THREAD_ID" }

var sessionIDRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{3,256}$`)

func (c *codex) NewSessionID() (string, error) {
	out, err := ExecRunner([]string{
		"exec",
		"--json",
		"--dangerously-bypass-approvals-and-sandbox",
		"Reply with exactly: ready",
	})
	if err != nil {
		return "", fmt.Errorf("codex exec: %w", err)
	}
	id, err := ParseSessionID(out)
	if err != nil {
		return "", err
	}
	return id, c.ValidateSessionID(id)
}

func (c *codex) ValidateSessionID(s string) error {
	if !sessionIDRe.MatchString(s) || strings.ContainsAny(s, " \t\r\n") {
		return fmt.Errorf("not a valid codex session id: %q", s)
	}
	return nil
}

func (c *codex) ValidateSession(workDir, sessionID string) error {
	return nil
}

func (c *codex) LaunchCmd(sessionID, prompt string, opts harness.LaunchOpts) string {
	if opts.Inject != "" {
		prompt = prompt + "\n\n" + harness.InjectionMarker + "\n" + opts.Inject
	}
	return codexResumeCmd(sessionID, prompt, opts.SkipPermissions)
}

func (c *codex) ResumeCmd(sessionID string, opts harness.LaunchOpts) string {
	prompt := ""
	if opts.Inject != "" {
		prompt = harness.InjectionMarker + "\n" + opts.Inject
	}
	return codexResumeCmd(sessionID, prompt, opts.SkipPermissions)
}

func codexResumeCmd(sessionID, prompt string, skipPermissions bool) string {
	cmd := "codex resume"
	if skipPermissions {
		cmd += " --dangerously-bypass-approvals-and-sandbox"
	}
	cmd += " " + sessionID
	if prompt != "" {
		cmd += " " + spawner.ShellQuote(prompt)
	}
	return cmd
}

func (c *codex) SkipPermissionsRun(prompt string) error {
	return SkipPermissionsRunner(prompt)
}

func runCodexExec(args []string) ([]byte, error) {
	return exec.Command("codex", args...).CombinedOutput()
}

func runSkipPermissions(prompt string) error {
	cmd := exec.Command("codex", "exec", "--dangerously-bypass-approvals-and-sandbox", prompt)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

func (c *codex) LiveSessionIDs() (map[string]int, error) {
	out, err := PSRunner()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	live := map[string]int{}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "resume") {
			continue
		}
		fields := strings.Fields(line)
		codexIndex := -1
		for i, f := range fields {
			if filepath.Base(f) == "codex" {
				codexIndex = i
				break
			}
		}
		if codexIndex < 0 {
			continue
		}
		for i, f := range fields[codexIndex+1:] {
			if f != "resume" {
				continue
			}
			for _, candidate := range fields[codexIndex+1+i+1:] {
				if strings.HasPrefix(candidate, "-") {
					continue
				}
				if sessionIDRe.MatchString(candidate) {
					live[strings.ToLower(candidate)]++
				}
				break
			}
		}
	}
	return live, nil
}

func runPS() ([]byte, error) {
	return exec.Command("ps", "-axo", "pid,command").Output()
}

func (c *codex) SkillInstallPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".codex", "skills", "flow", "SKILL.md"), nil
}

func (c *codex) SkillVersionPath() (string, error) {
	skill, err := c.SkillInstallPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(skill), "VERSION"), nil
}

func (c *codex) InstallSkill(content []byte) error {
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

func (c *codex) UninstallSkill() error {
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

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

func hooksPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	return filepath.Join(home, ".codex", "hooks.json"), nil
}

func (c *codex) InstallSessionStartHook(command string) (bool, error) {
	configChanged, err := ensureHooksFeature()
	if err != nil {
		return false, err
	}
	hookChanged, err := installHook("SessionStart", hookMatcher, command)
	return configChanged || hookChanged, err
}

func (c *codex) UninstallSessionStartHook(command string) (bool, error) {
	return uninstallHook("SessionStart", command)
}

func (c *codex) UninstallUserPromptSubmitHook(command string) (bool, error) {
	return uninstallHook("UserPromptSubmit", command)
}

func ensureHooksFeature() (bool, error) {
	path, err := configPath()
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("read %s: %w", path, err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
		}
		raw = nil
	}
	text := string(raw)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "codex_hooks") {
			if strings.Contains(trim, "true") {
				return false, nil
			}
			lines[i] = "codex_hooks = true"
			return writeConfig(path, lines)
		}
	}
	for i, line := range lines {
		if strings.TrimSpace(line) == "[features]" {
			lines = append(lines[:i+1], append([]string{"codex_hooks = true"}, lines[i+1:]...)...)
			return writeConfig(path, lines)
		}
	}
	if strings.TrimSpace(text) != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += "\n[features]\ncodex_hooks = true\n"
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

func writeConfig(path string, lines []string) (bool, error) {
	out := strings.Join(lines, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

func installHook(event, matcher, command string) (bool, error) {
	path, err := hooksPath()
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
	return writeHookSettings(path, settings)
}

func uninstallHook(event, command string) (bool, error) {
	path, err := hooksPath()
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
		filtered := make([]any, 0, len(inner))
		for _, h := range inner {
			hm, ok := h.(map[string]any)
			if !ok {
				filtered = append(filtered, h)
				continue
			}
			cmd, _ := hm["command"].(string)
			if strings.TrimSpace(cmd) == command {
				changed = true
				continue
			}
			filtered = append(filtered, h)
		}
		if len(filtered) == 0 {
			changed = true
			continue
		}
		m["hooks"] = filtered
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
	return writeHookSettings(path, settings)
}

func writeHookSettings(path string, settings map[string]any) (bool, error) {
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
