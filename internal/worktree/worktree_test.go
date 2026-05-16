package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo seeds a temp git repo with one commit on branch "main" and
// returns its absolute path.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "-c", "user.email=flow@example.test", "-c", "user.name=Flow Test", "commit", "-m", "init")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func TestEnsureCreatesWorktreeAndBranch(t *testing.T) {
	repo := initRepo(t)

	res, err := Ensure(repo, AgentClaude, "my-task")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !res.IsRepo {
		t.Fatal("IsRepo = false, want true for a real git repo")
	}
	if !res.Created {
		t.Fatal("Created = false, want true on first Ensure")
	}
	// macOS symlinks /var → /private/var, so derive expected from the
	// canonical RepoRoot the package will see, not the t.TempDir literal.
	wantPath := filepath.Join(RepoRoot(repo), ".claude", "worktrees", "my-task")
	if res.WorktreePath != wantPath {
		t.Errorf("WorktreePath = %q, want %q", res.WorktreePath, wantPath)
	}
	if res.Branch != "flow/my-task" {
		t.Errorf("Branch = %q, want flow/my-task", res.Branch)
	}
	if res.BaseBranch != "main" {
		t.Errorf("BaseBranch = %q, want main", res.BaseBranch)
	}

	// Worktree path must exist on disk and be on the new branch.
	if _, err := os.Stat(res.WorktreePath); err != nil {
		t.Fatalf("worktree path missing: %v", err)
	}
	gotBranch := runGit(t, res.WorktreePath, "branch", "--show-current")
	if gotBranch != "flow/my-task" {
		t.Errorf("worktree branch = %q, want flow/my-task", gotBranch)
	}

	// .gitignore on the host should now contain the worktrees pattern.
	gi, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !strings.Contains(string(gi), "/.claude/worktrees/") {
		t.Errorf(".gitignore did not get worktrees pattern; got:\n%s", string(gi))
	}
}

func TestEnsureReusesExistingWorktree(t *testing.T) {
	repo := initRepo(t)

	first, err := Ensure(repo, AgentClaude, "reuse-task")
	if err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	if !first.Created {
		t.Fatal("first call should report Created")
	}

	second, err := Ensure(repo, AgentClaude, "reuse-task")
	if err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if second.Created {
		t.Error("second Ensure should not report Created — worktree already exists")
	}
	if second.WorktreePath != first.WorktreePath {
		t.Errorf("second path %q != first %q", second.WorktreePath, first.WorktreePath)
	}
}

func TestEnsureNonRepoReturnsIsRepoFalse(t *testing.T) {
	dir := t.TempDir() // not a git repo
	res, err := Ensure(dir, AgentClaude, "no-repo")
	if err != nil {
		t.Fatalf("Ensure on non-repo: %v", err)
	}
	if res.IsRepo {
		t.Fatal("IsRepo = true on non-repo dir, want false")
	}
	if res.WorktreePath != dir {
		t.Errorf("WorktreePath = %q, want raw dir %q", res.WorktreePath, dir)
	}
}

func TestEnsureCodexUsesCodexSubdir(t *testing.T) {
	repo := initRepo(t)

	res, err := Ensure(repo, AgentCodex, "agent-codex")
	if err != nil {
		t.Fatalf("Ensure codex: %v", err)
	}
	wantPath := filepath.Join(RepoRoot(repo), ".codex", "worktrees", "agent-codex")
	if res.WorktreePath != wantPath {
		t.Errorf("WorktreePath = %q, want %q (codex prefix)", res.WorktreePath, wantPath)
	}
	gi, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if !strings.Contains(string(gi), "/.codex/worktrees/") {
		t.Errorf(".gitignore missing codex pattern; got:\n%s", string(gi))
	}
}

func TestEnsureRejectsUnknownAgent(t *testing.T) {
	repo := initRepo(t)
	if _, err := Ensure(repo, "fakeagent", "x"); err == nil {
		t.Fatal("expected error for unknown agent")
	}
}

func TestEnsureRejectsEmptySlug(t *testing.T) {
	repo := initRepo(t)
	if _, err := Ensure(repo, AgentClaude, ""); err == nil {
		t.Fatal("expected error for empty slug")
	}
}

func TestEnsureUnregisteredDirCollisionErrors(t *testing.T) {
	repo := initRepo(t)
	// Create the worktree directory by hand WITHOUT git knowing about it.
	collide := filepath.Join(repo, ".claude", "worktrees", "collide")
	if err := os.MkdirAll(collide, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collide, "intruder.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Ensure(repo, AgentClaude, "collide")
	if err == nil {
		t.Fatal("expected error when path exists but is not a registered worktree")
	}
	if !strings.Contains(err.Error(), "not a registered git worktree") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBaseBranchPrefersOriginHEAD(t *testing.T) {
	repo := initRepo(t)
	// Set up a fake origin remote pointing at ourselves and an
	// origin/HEAD ref pointing at a branch named "develop".
	runGit(t, repo, "branch", "develop")
	runGit(t, repo, "remote", "add", "origin", repo)
	// Manually write the symbolic-ref. The empty `origin/HEAD` ref must
	// exist for `symbolic-ref` to succeed.
	if err := os.MkdirAll(filepath.Join(repo, ".git", "refs", "remotes", "origin"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "update-ref", "refs/remotes/origin/develop", "HEAD")
	runGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/develop")

	if got := BaseBranch(repo); got != "develop" {
		t.Errorf("BaseBranch = %q, want develop", got)
	}
}

func TestBaseBranchFallsBackToMaster(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "master")
	if err := os.WriteFile(filepath.Join(dir, "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "x")
	runGit(t, dir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "x")
	if got := BaseBranch(dir); got != "master" {
		t.Errorf("BaseBranch = %q, want master", got)
	}
}

func TestEnsureSecondGitignoreCallIdempotent(t *testing.T) {
	repo := initRepo(t)
	if _, err := Ensure(repo, AgentClaude, "first"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if _, err := Ensure(repo, AgentClaude, "second"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if string(before) != string(after) {
		t.Errorf(".gitignore was rewritten on second call; want idempotent.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
