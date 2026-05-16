package app

import (
	"database/sql"
	"errors"
	"flow/internal/flowdb"
	"flow/internal/worktree"
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

	if rc := cmdAdd([]string{"task", "Worktree Demo", "--work-dir", repo}); rc != 0 {
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

func TestCmdDoNoWorktreeFlagSkipsWorktree(t *testing.T) {
	setupFlowRoot(t)
	stubITerm(t)
	repo := initGitRepoForWorktreeTest(t)

	if rc := cmdAdd([]string{"task", "Skip Worktree", "--work-dir", repo}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	if rc := cmdDo([]string{"skip-worktree", "--no-worktree"}); rc != 0 {
		t.Fatalf("do rc=%d", rc)
	}

	if _, err := os.Stat(filepath.Join(repo, ".claude", "worktrees", "skip-worktree")); err == nil {
		t.Error("worktree was created despite --no-worktree")
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "skip-worktree")
	if err != nil {
		t.Fatal(err)
	}
	if task.WorktreePath.Valid && task.WorktreePath.String != "" {
		t.Errorf("worktree_path was set to %q with --no-worktree; want unset", task.WorktreePath.String)
	}
}

func TestCmdDoNonRepoFallsThrough(t *testing.T) {
	setupFlowRoot(t)
	stubITerm(t)
	// Floating task -> auto-workspace, not a git repo.
	if rc := cmdAdd([]string{"task", "Non Repo"}); rc != 0 {
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

func TestRaiseDonePRForTask_PushesAndCreatesPR(t *testing.T) {
	setupFlowRoot(t)
	repo := initGitRepoForWorktreeTest(t)

	// Create the worktree directly via the package so we don't need to
	// drive cmdDo.
	wt, err := worktree.Ensure(repo, worktree.AgentClaude, "ship-it")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Add a commit on the worktree branch so there's something to PR.
	if err := os.WriteFile(filepath.Join(wt.WorktreePath, "feat.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForWorktreeTest(t, wt.WorktreePath, "add", "feat.txt")
	runGitForWorktreeTest(t, wt.WorktreePath, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "feat")

	task := &flowdb.Task{
		Slug:         "ship-it",
		Name:         "Ship It",
		WorktreePath: nullString(wt.WorktreePath),
	}

	pushCalled := false
	createCalled := false
	oldGit := gitCmdRunner
	gitCmdRunner = func(cwd string, args ...string) (string, error) {
		if len(args) >= 2 && args[0] == "push" {
			pushCalled = true
			return "", nil
		}
		// Defer to the real git for read-only inspection (branch, rev-list).
		return oldGit(cwd, args...)
	}
	t.Cleanup(func() { gitCmdRunner = oldGit })

	oldGh := ghCmdRunner
	ghCmdRunner = func(cwd string, args ...string) (string, error) {
		createCalled = true
		// Sanity: --base main --head flow/ship-it must be present.
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--base main") || !strings.Contains(joined, "--head flow/ship-it") {
			t.Errorf("gh args missing base/head: %v", args)
		}
		return "https://github.com/acme/widgets/pull/42", nil
	}
	t.Cleanup(func() { ghCmdRunner = oldGh })

	db := openFlowDB(t)
	// Seed the task into the DB so UpsertTaskPRLink's FK passes.
	if rc := cmdAdd([]string{"task", "Ship It", "--slug", "ship-it", "--work-dir", repo}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}

	prURL, err := raiseDonePRForTask(db, task)
	if err != nil {
		t.Fatalf("raiseDonePRForTask: %v", err)
	}
	if prURL != "https://github.com/acme/widgets/pull/42" {
		t.Errorf("prURL = %q, want acme/widgets/pull/42", prURL)
	}
	if !pushCalled {
		t.Error("git push was not invoked")
	}
	if !createCalled {
		t.Error("gh pr create was not invoked")
	}

	// Verify task_pr_links got the row.
	links, err := flowdb.ListTaskPRLinks(db, "ship-it")
	if err != nil {
		t.Fatalf("ListTaskPRLinks: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("got %d PR links, want 1", len(links))
	}
	if links[0].PRNumber != 42 || links[0].Repo != "acme/widgets" {
		t.Errorf("link = %+v, want repo=acme/widgets pr=42", links[0])
	}
}

func TestRaiseDonePRForTask_NoCommitsAheadSkips(t *testing.T) {
	setupFlowRoot(t)
	repo := initGitRepoForWorktreeTest(t)
	wt, err := worktree.Ensure(repo, worktree.AgentClaude, "no-commits")
	if err != nil {
		t.Fatal(err)
	}

	task := &flowdb.Task{Slug: "no-commits", Name: "No Commits", WorktreePath: nullString(wt.WorktreePath)}

	pushCalled := false
	oldGit := gitCmdRunner
	gitCmdRunner = func(cwd string, args ...string) (string, error) {
		if len(args) >= 1 && args[0] == "push" {
			pushCalled = true
		}
		return oldGit(cwd, args...)
	}
	t.Cleanup(func() { gitCmdRunner = oldGit })
	oldGh := ghCmdRunner
	ghCmdRunner = func(cwd string, args ...string) (string, error) {
		t.Errorf("gh should not be invoked when branch has no commits ahead")
		return "", errors.New("unreachable")
	}
	t.Cleanup(func() { ghCmdRunner = oldGh })

	db := openFlowDB(t)
	prURL, err := raiseDonePRForTask(db, task)
	if err != nil {
		t.Fatalf("raiseDonePRForTask: %v", err)
	}
	if prURL != "" {
		t.Errorf("prURL = %q, want empty (no commits ahead)", prURL)
	}
	if pushCalled {
		t.Error("git push should not be invoked when branch has no commits ahead")
	}
}

func TestRaiseDonePRForTask_NoWorktreeIsNoop(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	task := &flowdb.Task{Slug: "noop", Name: "Noop"}
	prURL, err := raiseDonePRForTask(db, task)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if prURL != "" {
		t.Errorf("prURL = %q, want empty for task without worktree", prURL)
	}
}

func TestCmdDoneNoPRFlagSkipsPRCreation(t *testing.T) {
	setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	repo := initGitRepoForWorktreeTest(t)
	wt, err := worktree.Ensure(repo, worktree.AgentClaude, "skip-pr")
	if err != nil {
		t.Fatal(err)
	}
	// Add a commit on the worktree branch so PR creation WOULD fire if
	// not for --no-pr.
	if err := os.WriteFile(filepath.Join(wt.WorktreePath, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForWorktreeTest(t, wt.WorktreePath, "add", "x.txt")
	runGitForWorktreeTest(t, wt.WorktreePath, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "x")

	if rc := cmdAdd([]string{"task", "Skip PR", "--slug", "skip-pr", "--work-dir", repo}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET worktree_path=?, session_id=?, session_started=? WHERE slug='skip-pr'`,
		wt.WorktreePath, fakeSessionID("skip-pr"), flowdb.NowISO(),
	); err != nil {
		t.Fatal(err)
	}
	db.Close()

	oldGh := ghCmdRunner
	ghCmdRunner = func(cwd string, args ...string) (string, error) {
		t.Errorf("gh should not be invoked under --no-pr; got args=%v", args)
		return "", errors.New("unreachable")
	}
	t.Cleanup(func() { ghCmdRunner = oldGh })

	if rc := cmdDone([]string{"skip-pr", "--no-pr"}); rc != 0 {
		t.Fatalf("done rc=%d", rc)
	}
}

func TestParsePRURL(t *testing.T) {
	repo, n := parsePRURL("https://github.com/acme/widgets/pull/123")
	if repo != "acme/widgets" || n != 123 {
		t.Errorf("got (%q, %d), want (acme/widgets, 123)", repo, n)
	}
	if r, n := parsePRURL("https://gitlab.example/some/path"); r != "" || n != 0 {
		t.Errorf("non-GitHub url returned %q/%d, want zero values", r, n)
	}
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: true}
}
