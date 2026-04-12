package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeGitConfig creates a .git/config under dir with the given origin
// URL. Returns the absolute dir path.
func writeFakeGitConfig(t *testing.T, dir, originURL string) {
	t.Helper()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `[core]
	repositoryformatversion = 0
[remote "origin"]
	url = ` + originURL + `
	fetch = +refs/heads/*:refs/remotes/origin/*
[branch "main"]
	remote = origin
`
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCmdWorkdirAddManualName(t *testing.T) {
	root := setupArchiveTestEnv(t)
	db := reopenArchiveTestDB(t, root)

	wdPath := filepath.Join(t.TempDir(), "myrepo")
	if err := os.MkdirAll(wdPath, 0o755); err != nil {
		t.Fatal(err)
	}

	if rc := cmdWorkdir([]string{"add", wdPath, "--name", "myrepo"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	w, err := GetWorkdir(db, wdPath)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !w.Name.Valid || w.Name.String != "myrepo" {
		t.Errorf("name: got %+v", w.Name)
	}
	if w.GitRemote.Valid && w.GitRemote.String != "" {
		t.Errorf("git_remote should be empty for non-git dir, got %+v", w.GitRemote)
	}
}

func TestCmdWorkdirAddDetectsGitRemote(t *testing.T) {
	root := setupArchiveTestEnv(t)
	db := reopenArchiveTestDB(t, root)

	wdPath := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(wdPath, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeGitConfig(t, wdPath, "git@github.com:foo/bar.git")

	if rc := cmdWorkdir([]string{"add", wdPath}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	w, err := GetWorkdir(db, wdPath)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !w.GitRemote.Valid || w.GitRemote.String != "git@github.com:foo/bar.git" {
		t.Errorf("git_remote: got %+v", w.GitRemote)
	}
}

func TestCmdWorkdirAddRequiresExistingPath(t *testing.T) {
	setupArchiveTestEnv(t)
	if rc := cmdWorkdir([]string{"add", "/definitely/not/here/xyz"}); rc == 0 {
		t.Errorf("expected error for non-existent path")
	}
}

func TestCmdWorkdirList(t *testing.T) {
	root := setupArchiveTestEnv(t)
	db := reopenArchiveTestDB(t, root)

	a := filepath.Join(t.TempDir(), "a")
	b := filepath.Join(t.TempDir(), "b")
	for _, p := range []string{a, b} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if rc := cmdWorkdir([]string{"add", a}); rc != 0 {
		t.Fatalf("add a rc=%d", rc)
	}
	if rc := cmdWorkdir([]string{"add", b}); rc != 0 {
		t.Fatalf("add b rc=%d", rc)
	}

	// Force a to be older so b comes first.
	if _, err := db.Exec("UPDATE workdirs SET last_used_at='2000-01-01T00:00:00Z' WHERE path=?", a); err != nil {
		t.Fatal(err)
	}
	list, err := ListWorkdirs(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 workdirs, got %d", len(list))
	}
	if list[0].Path != b || list[1].Path != a {
		t.Errorf("order: got [%s, %s], want [%s, %s]", list[0].Path, list[1].Path, b, a)
	}

	// Invoke the list command to ensure it doesn't crash on real rows.
	if rc := cmdWorkdir([]string{"list"}); rc != 0 {
		t.Errorf("list rc=%d", rc)
	}
}

func TestCmdWorkdirRemove(t *testing.T) {
	root := setupArchiveTestEnv(t)
	db := reopenArchiveTestDB(t, root)

	p := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if rc := cmdWorkdir([]string{"add", p}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	if rc := cmdWorkdir([]string{"remove", p}); rc != 0 {
		t.Fatalf("remove rc=%d", rc)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM workdirs WHERE path = ?", p).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("row still present after remove")
	}
	// Filesystem untouched.
	if _, err := os.Stat(p); err != nil {
		t.Errorf("directory removed from filesystem: %v", err)
	}
}

func TestCmdWorkdirScanFindsRepos(t *testing.T) {
	setupArchiveTestEnv(t)
	// Layout:
	//   scanRoot/
	//     a/.git/config  (depth 1)
	//     b/c/.git/config (depth 2)
	//     d/e/f/.git/config (depth 3)
	//     g/h/i/j/.git/config (depth 4 — should NOT be found)
	scanRoot := t.TempDir()
	paths := map[string]int{
		filepath.Join(scanRoot, "a"):             1,
		filepath.Join(scanRoot, "b", "c"):        2,
		filepath.Join(scanRoot, "d", "e", "f"):   3,
		filepath.Join(scanRoot, "g", "h", "i", "j"): 4,
	}
	for p := range paths {
		writeFakeGitConfig(t, p, "git@github.com:x/"+filepath.Base(p)+".git")
	}

	found, err := scanForGitRepos(scanRoot, 3)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	foundSet := map[string]bool{}
	for _, p := range found {
		foundSet[p] = true
	}
	for p, depth := range paths {
		want := depth <= 3
		got := foundSet[p]
		if want != got {
			t.Errorf("path %s (depth %d): want found=%v, got found=%v", p, depth, want, got)
		}
	}
}

func TestCmdWorkdirScanNoAdd(t *testing.T) {
	setupArchiveTestEnv(t)
	scanRoot := t.TempDir()
	repo := filepath.Join(scanRoot, "myrepo")
	writeFakeGitConfig(t, repo, "git@github.com:x/myrepo.git")

	if rc := cmdWorkdir([]string{"scan", scanRoot}); rc != 0 {
		t.Fatalf("scan rc=%d", rc)
	}
}

func TestCmdWorkdirScanAdd(t *testing.T) {
	root := setupArchiveTestEnv(t)
	db := reopenArchiveTestDB(t, root)

	scanRoot := t.TempDir()
	repo := filepath.Join(scanRoot, "myrepo")
	writeFakeGitConfig(t, repo, "git@github.com:x/myrepo.git")

	if rc := cmdWorkdir([]string{"scan", scanRoot, "--add"}); rc != 0 {
		t.Fatalf("scan --add rc=%d", rc)
	}
	w, err := GetWorkdir(db, repo)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !w.GitRemote.Valid || !strings.Contains(w.GitRemote.String, "myrepo.git") {
		t.Errorf("git_remote not registered: %+v", w.GitRemote)
	}
	if !w.Name.Valid || w.Name.String != "myrepo" {
		t.Errorf("name not registered: %+v", w.Name)
	}
}

func TestCmdWorkdirUnknownSubcommand(t *testing.T) {
	setupArchiveTestEnv(t)
	if rc := cmdWorkdir([]string{}); rc != 2 {
		t.Errorf("empty args rc=%d, want 2", rc)
	}
	if rc := cmdWorkdir([]string{"wat"}); rc != 2 {
		t.Errorf("bad subcommand rc=%d, want 2", rc)
	}
}

func TestDetectGitRemoteMissing(t *testing.T) {
	dir := t.TempDir()
	if got := detectGitRemote(dir); got != "" {
		t.Errorf("want empty for non-git dir, got %q", got)
	}
}

func TestDetectGitRemoteBasic(t *testing.T) {
	dir := t.TempDir()
	writeFakeGitConfig(t, dir, "https://github.com/foo/bar.git")
	got := detectGitRemote(dir)
	if got != "https://github.com/foo/bar.git" {
		t.Errorf("want https://github.com/foo/bar.git, got %q", got)
	}
}
