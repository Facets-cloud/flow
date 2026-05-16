package agenthooks

import (
	"encoding/json"
	"flow/internal/flowdb"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallLocalWritesRepoScopedHookFiles(t *testing.T) {
	dir := t.TempDir()
	changed, err := InstallLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("InstallLocal changed=false, want true on first install")
	}

	claude := readHookTestFile(t, filepath.Join(dir, ".claude", "settings.local.json"))
	if !hookFileReferences(claude, "Notification", ClaudeCommand) {
		t.Fatalf("Claude local hook missing Notification command: %#v", claude["hooks"])
	}
	if !hookFileReferences(claude, "PermissionRequest", ClaudeCommand) {
		t.Fatalf("Claude local hook missing PermissionRequest command: %#v", claude["hooks"])
	}

	codex := readHookTestFile(t, filepath.Join(dir, ".codex", "hooks.json"))
	if !hookFileReferences(codex, "PermissionRequest", CodexCommand) {
		t.Fatalf("Codex local hook missing PermissionRequest command: %#v", codex["hooks"])
	}
	if !hookFileReferences(codex, "PreToolUse", CodexCommand) {
		t.Fatalf("Codex local hook missing PreToolUse command: %#v", codex["hooks"])
	}
	if !hookFileReferences(codex, "PostToolUse", CodexCommand) {
		t.Fatalf("Codex local hook missing PostToolUse command: %#v", codex["hooks"])
	}
}

func TestInstallLocalIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if changed, err := InstallLocal(dir); err != nil || !changed {
		t.Fatalf("first install changed=%v err=%v, want changed", changed, err)
	}
	if changed, err := InstallLocal(dir); err != nil || changed {
		t.Fatalf("second install changed=%v err=%v, want no change", changed, err)
	}
}

func TestInstallLocalPreservesExistingHooks(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"hooks":{"Notification":[{"hooks":[{"type":"command","command":"custom-notifier"}]}]},"theme":"dark"}`)
	if err := os.WriteFile(settings, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := InstallLocal(dir); err != nil {
		t.Fatal(err)
	}
	claude := readHookTestFile(t, settings)
	if !hookFileReferences(claude, "Notification", "custom-notifier") {
		t.Fatalf("existing hook was not preserved: %#v", claude["hooks"])
	}
	if !hookFileReferences(claude, "Notification", ClaudeCommand) {
		t.Fatalf("flow hook was not added: %#v", claude["hooks"])
	}
	if got, _ := claude["theme"].(string); got != "dark" {
		t.Fatalf("top-level field = %q, want dark", got)
	}
}

func TestInstallLocalExcludesGeneratedHookFilesFromGitStatus(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}

	if _, err := InstallLocal(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	exclude := string(raw)
	for _, want := range []string{".claude/settings.local.json", ".codex/hooks.json"} {
		if !strings.Contains(exclude, want) {
			t.Fatalf("git exclude missing %q:\n%s", want, exclude)
		}
	}
}

func TestInstallKnownWorkdirsAddsHooksForExistingRecords(t *testing.T) {
	db, err := flowdb.OpenDB(filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	taskDir := t.TempDir()
	worktreeDir := t.TempDir()
	projectDir := t.TempDir()
	playbookDir := t.TempDir()
	registryDir := t.TempDir()
	now := flowdb.NowISO()
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, work_dir, worktree_path, session_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"task", "Task", taskDir, worktreeDir, "session-1", now, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO projects (slug, name, work_dir, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		"project", "Project", projectDir, now, now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO playbooks (slug, name, work_dir, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		"playbook", "Playbook", playbookDir, now, now,
	); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.UpsertWorkdir(db, registryDir, "registry", "", ""); err != nil {
		t.Fatal(err)
	}

	changed, err := InstallKnownWorkdirs(db)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 5 {
		t.Fatalf("changed = %d, want 5", changed)
	}
	for _, dir := range []string{taskDir, worktreeDir, projectDir, playbookDir, registryDir} {
		claude := readHookTestFile(t, filepath.Join(dir, ".claude", "settings.local.json"))
		if !hookFileReferences(claude, "PermissionRequest", ClaudeCommand) {
			t.Fatalf("%s missing Claude hook", dir)
		}
		codex := readHookTestFile(t, filepath.Join(dir, ".codex", "hooks.json"))
		if !hookFileReferences(codex, "PermissionRequest", CodexCommand) {
			t.Fatalf("%s missing Codex hook", dir)
		}
	}

	changed, err = InstallKnownWorkdirs(db)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatalf("second install changed = %d, want 0", changed)
	}
}

func readHookTestFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, raw)
	}
	return cfg
}

func hookFileReferences(cfg map[string]any, event, command string) bool {
	hooks, _ := cfg["hooks"].(map[string]any)
	entries, _ := hooks[event].([]any)
	for _, entry := range entries {
		m, _ := entry.(map[string]any)
		inner, _ := m["hooks"].([]any)
		for _, hook := range inner {
			hm, _ := hook.(map[string]any)
			if cmd, _ := hm["command"].(string); cmd == command {
				return true
			}
		}
	}
	return false
}
