package agenthooks

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	ClaudeCommand = "flow hook agent-event --provider claude"
	CodexCommand  = "flow hook agent-event --provider codex"
)

type spec struct {
	event   string
	matcher string
}

var claudeHooks = []spec{
	{event: "SessionStart", matcher: "startup|resume"},
	{event: "UserPromptSubmit"},
	{event: "PermissionRequest"},
	{event: "PermissionDenied"},
	{event: "Notification"},
	{event: "Elicitation"},
	{event: "ElicitationResult"},
	{event: "PreToolUse", matcher: "AskUserQuestion|ExitPlanMode"},
	{event: "PostToolUse"},
	{event: "PostToolUseFailure"},
	{event: "PostToolBatch"},
	{event: "Stop"},
	{event: "StopFailure"},
	{event: "SessionEnd"},
	{event: "TeammateIdle"},
	{event: "SubagentStart"},
	{event: "SubagentStop"},
	{event: "TaskCreated"},
	{event: "TaskCompleted"},
}

var codexHooks = []spec{
	{event: "SessionStart", matcher: "startup|resume|clear"},
	{event: "UserPromptSubmit"},
	{event: "PreToolUse", matcher: "AskUserQuestion|ExitPlanMode|request_user_input|mcp__.*request_user_input"},
	{event: "PermissionRequest"},
	{event: "PostToolUse"},
	{event: "Stop"},
}

func InstallLocal(workDir string) (bool, error) {
	root := strings.TrimSpace(workDir)
	if root == "" {
		return false, fmt.Errorf("workdir is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	if st, err := os.Stat(abs); err != nil {
		return false, err
	} else if !st.IsDir() {
		return false, fmt.Errorf("%s is not a directory", abs)
	}

	changed := false
	claudePath := filepath.Join(abs, ".claude", "settings.local.json")
	for _, hook := range claudeHooks {
		added, err := installHook(claudePath, hook.event, hook.matcher, ClaudeCommand, nil)
		if err != nil {
			return changed, err
		}
		changed = changed || added
	}

	codexPath := filepath.Join(abs, ".codex", "hooks.json")
	codexExtras := map[string]any{"timeout": 3, "statusMessage": "Syncing flow status"}
	for _, hook := range codexHooks {
		added, err := installHook(codexPath, hook.event, hook.matcher, CodexCommand, codexExtras)
		if err != nil {
			return changed, err
		}
		changed = changed || added
	}
	if err := excludeLocalHookFiles(abs); err != nil {
		return changed, err
	}
	return changed, nil
}

func InstallKnownWorkdirs(db *sql.DB) (int, error) {
	if db == nil {
		return 0, nil
	}
	paths := map[string]bool{}
	queries := []string{
		`SELECT work_dir FROM tasks WHERE deleted_at IS NULL`,
		`SELECT worktree_path FROM tasks WHERE worktree_path IS NOT NULL AND worktree_path != '' AND deleted_at IS NULL`,
		`SELECT work_dir FROM projects WHERE deleted_at IS NULL`,
		`SELECT work_dir FROM playbooks WHERE deleted_at IS NULL`,
		`SELECT path FROM workdirs`,
	}
	for _, query := range queries {
		rows, err := db.Query(query)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var path sql.NullString
			if err := rows.Scan(&path); err != nil {
				rows.Close()
				return 0, err
			}
			if path.Valid && strings.TrimSpace(path.String) != "" {
				paths[path.String] = true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return 0, err
		}
		rows.Close()
	}

	changed := 0
	var errs []error
	for path := range paths {
		didChange, err := InstallLocal(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			errs = append(errs, err)
			continue
		}
		if didChange {
			changed++
		}
	}
	return changed, errors.Join(errs...)
}

func installHook(path, event, matcher, command string, extras map[string]any) (bool, error) {
	cfg, err := readHookConfig(path)
	if err != nil {
		return false, err
	}
	hooks, _ := cfg["hooks"].(map[string]any)
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

	hookEntry := map[string]any{"type": "command", "command": command}
	for k, v := range extras {
		hookEntry[k] = v
	}
	group := map[string]any{"hooks": []any{hookEntry}}
	if strings.TrimSpace(matcher) != "" {
		group["matcher"] = matcher
	}
	entries = append(entries, group)
	hooks[event] = entries
	cfg["hooks"] = hooks
	return true, writeHookConfig(path, cfg)
}

func readHookConfig(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		return map[string]any{}, nil
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return cfg, nil
}

func writeHookConfig(path string, cfg map[string]any) error {
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	out = append(out, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func excludeLocalHookFiles(workDir string) error {
	excludePath, err := gitExcludePath(workDir)
	if err != nil || excludePath == "" {
		return nil
	}
	raw, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read git exclude %s: %w", excludePath, err)
	}
	existing := string(raw)
	lines := []string{".claude/settings.local.json", ".codex/hooks.json"}
	add := []string{}
	for _, line := range lines {
		if !containsExcludeLine(existing, line) {
			add = append(add, line)
		}
	}
	if len(add) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return fmt.Errorf("mkdir git exclude dir %s: %w", filepath.Dir(excludePath), err)
	}
	prefix := ""
	if len(raw) > 0 && !strings.HasSuffix(existing, "\n") {
		prefix = "\n"
	}
	content := prefix + strings.Join(add, "\n") + "\n"
	f, err := os.OpenFile(excludePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open git exclude %s: %w", excludePath, err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("write git exclude %s: %w", excludePath, err)
	}
	return nil
}

func gitExcludePath(workDir string) (string, error) {
	cmd := exec.Command("git", "-C", workDir, "rev-parse", "--git-path", "info/exclude")
	out, err := cmd.Output()
	if err != nil {
		return "", nil
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", nil
	}
	if filepath.IsAbs(path) {
		return path, nil
	}
	return filepath.Join(workDir, path), nil
}

func containsExcludeLine(content, line string) bool {
	for _, existing := range strings.Split(content, "\n") {
		if strings.TrimSpace(existing) == line {
			return true
		}
	}
	return false
}
