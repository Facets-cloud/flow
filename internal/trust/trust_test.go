package trust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func readJSON(t *testing.T, p string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse %s: %v\n%s", p, err, b)
	}
	return m
}

func trusted(m map[string]any, dir string) bool {
	projects, _ := m["projects"].(map[string]any)
	entry, _ := projects[dir].(map[string]any)
	v, _ := entry["hasTrustDialogAccepted"].(bool)
	return v
}

func TestEnsureClaudeTrustedCreatesMissingConfig(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), ".claude.json")
	changed, err := EnsureClaudeTrusted(cfg, "/x/tasks")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !trusted(readJSON(t, cfg), "/x/tasks") {
		t.Fatal("dir not trusted")
	}
	if fi, _ := os.Stat(cfg); fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, want 0600", fi.Mode().Perm())
	}
}

func TestEnsureClaudeTrustedPreservesOtherKeys(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), ".claude.json")
	orig := `{
  "userID": "abc",
  "numStartups": 12345678901234567890,
  "oauthAccount": {"email": "x"},
  "projects": {
    "/user/repo": {"hasTrustDialogAccepted": true, "allowedTools": ["Bash"], "lastCost": 1.5},
    "/x/tasks": {"allowedTools": [], "hasTrustDialogAccepted": false}
  }
}`
	if err := os.WriteFile(cfg, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := EnsureClaudeTrusted(cfg, "/x/tasks")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	b, _ := os.ReadFile(cfg)
	if !strings.Contains(string(b), "12345678901234567890") {
		t.Fatalf("large number not preserved verbatim:\n%s", b)
	}
	m := readJSON(t, cfg)
	if m["userID"] != "abc" {
		t.Fatalf("userID lost: %v", m["userID"])
	}
	if acct, _ := m["oauthAccount"].(map[string]any); acct["email"] != "x" {
		t.Fatalf("oauthAccount lost: %v", m["oauthAccount"])
	}
	p := m["projects"].(map[string]any)
	repo := p["/user/repo"].(map[string]any)
	if repo["lastCost"] != 1.5 || len(repo["allowedTools"].([]any)) != 1 {
		t.Fatalf("other project mangled: %v", repo)
	}
	tasks := p["/x/tasks"].(map[string]any)
	if tasks["hasTrustDialogAccepted"] != true {
		t.Fatal("not trusted")
	}
	if _, ok := tasks["allowedTools"]; !ok {
		t.Fatal("existing per-project keys on the target entry dropped")
	}
	if fi, _ := os.Stat(cfg); fi.Mode().Perm() != 0o644 {
		t.Fatalf("existing perm not preserved: %v", fi.Mode().Perm())
	}
}

func TestEnsureClaudeTrustedIdempotent(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), ".claude.json")
	orig := `{"projects":{"/x/tasks":{"hasTrustDialogAccepted":true}},"z":1}`
	os.WriteFile(cfg, []byte(orig), 0o600)
	changed, err := EnsureClaudeTrusted(cfg, "/x/tasks")
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v; want no-op", changed, err)
	}
	if b, _ := os.ReadFile(cfg); string(b) != orig {
		t.Fatalf("file rewritten on no-op:\n%s", b)
	}
}

func TestEnsureClaudeTrustedMalformedUntouched(t *testing.T) {
	for _, orig := range []string{`{"projects": {`, `[1,2]`, `{"projects": "nope"}`, `{"projects":{"/x/tasks":"nope"}}`} {
		cfg := filepath.Join(t.TempDir(), ".claude.json")
		os.WriteFile(cfg, []byte(orig), 0o600)
		changed, err := EnsureClaudeTrusted(cfg, "/x/tasks")
		if err == nil || changed {
			t.Fatalf("%q: want error, got changed=%v err=%v", orig, changed, err)
		}
		if !strings.Contains(err.Error(), cfg) {
			t.Fatalf("error should name the file: %v", err)
		}
		if b, _ := os.ReadFile(cfg); string(b) != orig {
			t.Fatalf("%q: malformed file modified: %s", orig, b)
		}
		entries, _ := os.ReadDir(filepath.Dir(cfg))
		for _, e := range entries {
			if e.Name() != ".claude.json" && !strings.HasSuffix(e.Name(), ".lock") {
				t.Fatalf("stray file left behind: %s", e.Name())
			}
		}
	}
}

func TestEnsureClaudeTrustedConcurrent(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), ".claude.json")
	os.WriteFile(cfg, []byte(`{"keep":true}`), 0o600)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := EnsureClaudeTrusted(cfg, fmt.Sprintf("/d/%d", i)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	m := readJSON(t, cfg)
	for i := 0; i < 20; i++ {
		if !trusted(m, fmt.Sprintf("/d/%d", i)) {
			t.Fatalf("lost update for /d/%d", i)
		}
	}
	if m["keep"] != true {
		t.Fatal("other key lost")
	}
}

func TestClaudeConfigPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if p, _ := ClaudeConfigPath(); p != filepath.Join(home, ".claude.json") {
		t.Fatalf("got %s", p)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "/cfg")
	if p, _ := ClaudeConfigPath(); p != "/cfg/.claude.json" {
		t.Fatalf("got %s", p)
	}
}

// --- FlowOwnedRoot ---

func realTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFlowOwnedRoot(t *testing.T) {
	base := realTemp(t)
	root := filepath.Join(base, "flowroot")
	ws := filepath.Join(root, "tasks", "t1", "workspace")
	repoWS := filepath.Join(root, "tasks", "t2", "workspace")
	userRepo := filepath.Join(base, "code", "repo")
	for _, d := range []string{ws, filepath.Join(repoWS, ".git"), filepath.Join(userRepo, ".git"), filepath.Join(root, "projects", "p")} {
		os.MkdirAll(d, 0o755)
	}
	tasks := filepath.Join(root, "tasks")

	cases := []struct {
		name, workDir, want string
		ok                  bool
	}{
		{"flow workspace", ws, tasks, true},
		{"tasks dir itself", tasks, tasks, true},
		{"user repo", userRepo, "", false},
		{"workspace that is a git repo", repoWS, "", false},
		{"subdir of git workspace", filepath.Join(repoWS, "sub"), "", false},
		{"flow root but outside tasks", filepath.Join(root, "projects", "p"), "", false},
		{"prefix trick", root + "/tasks-evil", "", false},
		{"empty", "", "", false},
	}
	os.MkdirAll(filepath.Join(repoWS, "sub"), 0o755)
	os.MkdirAll(root+"/tasks-evil", 0o755)
	for _, c := range cases {
		got, ok := FlowOwnedRoot(root, c.workDir)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got (%q,%v) want (%q,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestFlowOwnedRootResolvesSymlinks(t *testing.T) {
	base := realTemp(t)
	real := filepath.Join(base, "real")
	os.MkdirAll(filepath.Join(real, "tasks", "t", "workspace"), 0o755)
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, ok := FlowOwnedRoot(link, filepath.Join(link, "tasks", "t", "workspace"))
	if !ok || got != filepath.Join(real, "tasks") {
		t.Fatalf("got (%q,%v), want resolved %q", got, ok, filepath.Join(real, "tasks"))
	}
	// mixed: root via link, workdir via real path
	if got, ok := FlowOwnedRoot(link, filepath.Join(real, "tasks", "t", "workspace")); !ok || got != filepath.Join(real, "tasks") {
		t.Fatalf("mixed: got (%q,%v)", got, ok)
	}
}

func TestFlowOwnedRootInsideGitRepo(t *testing.T) {
	// A flow root the user turned into (or placed inside) a git repo:
	// Claude keys trust by the repo root, so trusting tasks/ would not
	// help and trusting the repo root is not flow's call — skip.
	base := realTemp(t)
	root := filepath.Join(base, "flowroot")
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	os.MkdirAll(filepath.Join(root, "tasks", "t", "workspace"), 0o755)
	if got, ok := FlowOwnedRoot(root, filepath.Join(root, "tasks", "t", "workspace")); ok {
		t.Fatalf("got %q; flow root inside git repo must not be trusted", got)
	}
}

func TestEnsureWorkspaceTrusted(t *testing.T) {
	base := realTemp(t)
	root := filepath.Join(base, "flowroot")
	ws := filepath.Join(root, "tasks", "t", "workspace")
	userRepo := filepath.Join(base, "repo")
	os.MkdirAll(ws, 0o755)
	os.MkdirAll(filepath.Join(userRepo, ".git"), 0o755)
	cfg := filepath.Join(base, ".claude.json")

	if changed, err := EnsureClaudeWorkspaceTrusted(cfg, root, userRepo); err != nil || changed {
		t.Fatalf("user repo: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatal("config written for a user repo")
	}
	if changed, err := EnsureClaudeWorkspaceTrusted(cfg, root, ws); err != nil || !changed {
		t.Fatalf("flow ws: changed=%v err=%v", changed, err)
	}
	m := readJSON(t, cfg)
	if !trusted(m, filepath.Join(root, "tasks")) {
		t.Fatalf("tasks root not trusted: %v", m)
	}
	if trusted(m, userRepo) || trusted(m, ws) {
		t.Fatal("trusted something other than the tasks root")
	}
}
