package app

import (
	"flow/internal/flowdb"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGitRepoForWorktreeTest spins up a minimal git repo (with one
// commit on branch "main") inside t.TempDir() and returns its canonical
// path. The canonical form survives macOS's /var → /private/var symlink
// — important because git rev-parse --show-toplevel emits the canonical
// path, so do.go's worktree resolution will produce paths anchored at
// /private/var/...
func initGitRepoForWorktreeTest(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGitForWorktreeTest(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForWorktreeTest(t, repo, "add", "README.md")
	runGitForWorktreeTest(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "init")
	if canon, err := exec.Command("git", "-C", repo, "rev-parse", "--show-toplevel").Output(); err == nil {
		return strings.TrimSpace(string(canon))
	}
	return repo
}

func runGitForWorktreeTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

func TestCmdDoCreatesWorktreeForGitRepo(t *testing.T) {
	setupFlowRoot(t)
	stubITerm(t)
	repo := initGitRepoForWorktreeTest(t)

	if rc := cmdAdd([]string{"task", "Worktree Demo", "--work-dir", repo, "--agent", "claude"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	if rc := cmdDo([]string{"worktree-demo"}); rc != 0 {
		t.Fatalf("do rc=%d", rc)
	}

	wantWT := filepath.Join(repo, ".claude", "worktrees", "worktree-demo")
	if _, err := os.Stat(wantWT); err != nil {
		t.Fatalf("worktree dir missing: %v", err)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "worktree-demo")
	if err != nil {
		t.Fatal(err)
	}
	if !task.WorktreePath.Valid || task.WorktreePath.String != wantWT {
		t.Errorf("worktree_path persisted as %v, want %q", task.WorktreePath, wantWT)
	}

	// The worktree must be on branch flow/<slug>.
	out, err := exec.Command("git", "-C", wantWT, "branch", "--show-current").Output()
	if err != nil {
		t.Fatalf("read branch: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "flow/worktree-demo" {
		t.Errorf("worktree branch = %q, want flow/worktree-demo", got)
	}
}

func TestCmdDoNoWorktreeFlagIsRejected(t *testing.T) {
	setupFlowRoot(t)
	stubITerm(t)
	repo := initGitRepoForWorktreeTest(t)

	if rc := cmdAdd([]string{"task", "Skip Worktree", "--work-dir", repo, "--agent", "claude"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	if rc := cmdDo([]string{"skip-worktree", "--no-worktree"}); rc != 2 {
		t.Fatalf("do rc=%d, want 2", rc)
	}

	if _, err := os.Stat(filepath.Join(repo, ".claude", "worktrees", "skip-worktree")); err == nil {
		t.Error("worktree was created after rejected --no-worktree")
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "skip-worktree")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "backlog" || task.SessionID.Valid {
		t.Fatalf("rejected --no-worktree should not start task: %+v", task)
	}
	if task.WorktreePath.Valid && task.WorktreePath.String != "" {
		t.Errorf("worktree_path was set to %q after rejected --no-worktree; want unset", task.WorktreePath.String)
	}
}

func TestCmdDoNonRepoFallsThrough(t *testing.T) {
	setupFlowRoot(t)
	stubITerm(t)
	// Floating task -> auto-workspace, not a git repo.
	if rc := cmdAdd([]string{"task", "Non Repo", "--agent", "claude"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	if rc := cmdDo([]string{"non-repo"}); rc != 0 {
		t.Fatalf("do rc=%d", rc)
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "non-repo")
	if err != nil {
		t.Fatal(err)
	}
	if task.WorktreePath.Valid && task.WorktreePath.String != "" {
		t.Errorf("worktree_path = %q for non-repo task; want unset", task.WorktreePath.String)
	}
}
