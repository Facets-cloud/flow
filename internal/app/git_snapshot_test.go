package app

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

func TestCmdDoCapturesGitStartSnapshot(t *testing.T) {
	root := setupFlowRoot(t)
	repo, head := initGitRepoForSnapshotTest(t)
	stubITerm(t)

	if rc := cmdAdd([]string{"task", "Git Start", "--work-dir", repo}); rc != 0 {
		t.Fatalf("add task rc=%d", rc)
	}
	if rc := cmdDo([]string{"git-start"}); rc != 0 {
		t.Fatalf("do rc=%d", rc)
	}

	raw, err := os.ReadFile(taskGitStartSnapshotPath(root, "git-start"))
	if err != nil {
		t.Fatalf("read git start snapshot: %v", err)
	}
	var snap taskGitStartSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal git start snapshot: %v", err)
	}
	if snap.TaskSlug != "git-start" {
		t.Fatalf("task slug = %q, want git-start", snap.TaskSlug)
	}
	// Since flow do now spawns into a per-task worktree, the snapshot
	// captures the worktree's working directory (the cwd the agent will
	// actually edit in), not the task's host work_dir. RepoRoot is
	// `git rev-parse --show-toplevel` *from inside the worktree*, which
	// returns the worktree's own toplevel — not the host repo's path.
	hostRoot := runGitForSnapshotTest(t, repo, "rev-parse", "--show-toplevel")
	wantWorkDir := filepath.Join(hostRoot, ".claude", "worktrees", "git-start")
	if snap.WorkDir != wantWorkDir {
		t.Fatalf("work_dir = %q, want %q", snap.WorkDir, wantWorkDir)
	}
	if snap.RepoRoot != wantWorkDir {
		t.Fatalf("repo_root = %q, want %q (worktree toplevel)", snap.RepoRoot, wantWorkDir)
	}
	// HEAD on the new worktree branch starts at the same commit as
	// base; flow worktree creation does `git worktree add -b flow/<slug>
	// <path> <base>`, so the worktree HEAD == base HEAD == `head`.
	if snap.Head != head {
		t.Fatalf("head = %q, want %q (worktree HEAD == base HEAD at creation)", snap.Head, head)
	}
	if snap.HeadShort == "" {
		t.Fatal("head_short should be recorded")
	}
}

func TestCmdDoneWritesGitCloseoutSnapshotUpdate(t *testing.T) {
	root := setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	repo, startHead := initGitRepoForSnapshotTest(t)

	if rc := cmdAdd([]string{"task", "Git Close", "--work-dir", repo}); rc != 0 {
		t.Fatalf("add task rc=%d", rc)
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "git-close")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := captureTaskGitStartSnapshot(task, false); err != nil {
		t.Fatalf("capture start snapshot: %v", err)
	}

	if err := os.WriteFile(filepath.Join(repo, "app.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForSnapshotTest(t, repo, "add", "app.go")
	runGitForSnapshotTest(t, repo, "-c", "user.email=flow@example.test", "-c", "user.name=Flow Test", "commit", "-m", "add app")
	endHead := runGitForSnapshotTest(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	db = openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=? WHERE slug=?`,
		fakeSessionID("git-close"), flowdb.NowISO(), "git-close",
	); err != nil {
		t.Fatal(err)
	}
	db.Close()

	out := captureStdout(t, func() {
		if rc := cmdDone([]string{"git-close"}); rc != 0 {
			t.Fatalf("done rc=%d", rc)
		}
	})
	if !strings.Contains(out, "Saved git snapshot ") {
		t.Fatalf("done output missing git snapshot path:\n%s", out)
	}

	updates, err := filepath.Glob(filepath.Join(root, "tasks", "git-close", "updates", "*-git-closeout.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 {
		t.Fatalf("git closeout updates = %d, want 1 (%v)", len(updates), updates)
	}
	md, err := os.ReadFile(updates[0])
	if err != nil {
		t.Fatal(err)
	}
	body := string(md)
	for _, want := range []string{
		"Start HEAD: " + startHead[:12],
		"End HEAD: " + endHead[:12],
		"A\tapp.go",
		"?? scratch.txt",
		"Metadata JSON: " + taskGitCloseoutSnapshotPath(root, "git-close"),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("closeout update missing %q:\n%s", want, body)
		}
	}

	raw, err := os.ReadFile(taskGitCloseoutSnapshotPath(root, "git-close"))
	if err != nil {
		t.Fatalf("read closeout json: %v", err)
	}
	var snap taskGitCloseoutSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("unmarshal closeout json: %v", err)
	}
	if snap.Start == nil || snap.Start.Head != startHead {
		t.Fatalf("start snapshot = %+v, want head %s", snap.Start, startHead)
	}
	if snap.EndHead != endHead {
		t.Fatalf("end head = %q, want %q", snap.EndHead, endHead)
	}
	if !snapshotLinesContain(snap.FilesChangedSinceStart, "A\tapp.go") {
		t.Fatalf("files changed since start = %#v, want app.go", snap.FilesChangedSinceStart)
	}
	if !snapshotLinesContain(snap.WorkingTreeStatus, "?? scratch.txt") {
		t.Fatalf("working tree status = %#v, want scratch.txt", snap.WorkingTreeStatus)
	}
}

func initGitRepoForSnapshotTest(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	runGitForSnapshotTest(t, repo, "init")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForSnapshotTest(t, repo, "add", "README.md")
	runGitForSnapshotTest(t, repo, "-c", "user.email=flow@example.test", "-c", "user.name=Flow Test", "commit", "-m", "initial")
	return repo, runGitForSnapshotTest(t, repo, "rev-parse", "HEAD")
}

func runGitForSnapshotTest(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-C", repo}, args...)
	cmd := exec.Command("git", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func snapshotLinesContain(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}
