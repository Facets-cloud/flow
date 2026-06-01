package server

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"flow/internal/flowdb"
)

// Claude derives its transcript path from the launch cwd. A worktree session's
// cwd is the worktree, not work_dir — resolveSessionJSONLPath must try the
// worktree first so the token-usage parse finds the file (otherwise it returns
// 0 and the UI shows the 1.2k estimate floor).
func TestResolveSessionJSONLPathPrefersWorktree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sid := "6451be34-8089-4b8c-81b4-456eb89501b5"
	repo := "/Users/x/repo"
	worktree := "/Users/x/.claude/worktrees/feature"

	// Only the worktree-encoded transcript exists on disk.
	wtDir := filepath.Join(home, ".claude", "projects", encodeCwdForClaude(worktree))
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wtFile := filepath.Join(wtDir, sid+".jsonl")
	if err := os.WriteFile(wtFile, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	task := &flowdb.Task{
		WorkDir:         repo,
		WorktreePath:    sql.NullString{String: worktree, Valid: true},
		SessionProvider: "claude",
		SessionID:       sql.NullString{String: sid, Valid: true},
	}
	got, err := resolveSessionJSONLPath(task)
	if err != nil {
		t.Fatalf("resolve err: %v", err)
	}
	if got != wtFile {
		t.Fatalf("got %q, want worktree path %q", got, wtFile)
	}
}

// A non-worktree Claude session still resolves under work_dir.
func TestResolveSessionJSONLPathFallsBackToWorkDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sid := "22222222-2222-4222-8222-222222222222"
	repo := filepath.Join(home, "code", "repo")

	dir := filepath.Join(home, ".claude", "projects", encodeCwdForClaude(repo))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(file, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	task := &flowdb.Task{
		WorkDir:         repo,
		SessionProvider: "claude",
		SessionID:       sql.NullString{String: sid, Valid: true},
	}
	got, err := resolveSessionJSONLPath(task)
	if err != nil {
		t.Fatalf("resolve err: %v", err)
	}
	if got != file {
		t.Fatalf("got %q, want work_dir path %q", got, file)
	}
}

// Codex (and any session with a captured session_path) resolves by stored path,
// independent of cwd — so a worktree never affects it. This is the same
// token-usage input path the Claude case now also reaches.
func TestSessionJSONLPathUsesStoredCodexPath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "codex-rollout.jsonl")
	if err := os.WriteFile(file, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	task := &flowdb.Task{
		WorkDir:         "/Users/x/.claude/worktrees/feature", // worktree cwd, irrelevant for codex
		SessionProvider: "codex",
		SessionID:       sql.NullString{String: "33333333-3333-4333-8333-333333333333", Valid: true},
		SessionPath:     sql.NullString{String: file, Valid: true},
	}
	got, err := sessionJSONLPath(nil, task)
	if err != nil {
		t.Fatalf("resolve err: %v", err)
	}
	if got != file {
		t.Fatalf("got %q, want stored codex path %q", got, file)
	}
}
