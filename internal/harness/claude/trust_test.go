package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/harness"
)

// trustHome points HOME at a temp dir (and clears CLAUDE_CONFIG_DIR) so
// no test ever touches the real ~/.claude.json.
func trustHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

func writeClaudeJSON(t *testing.T, home, body string) string {
	t.Helper()
	p := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func readClaudeJSON(t *testing.T, p string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("result is not valid JSON: %v\n%s", err, raw)
	}
	return m
}

func trusted(m map[string]any, dir string) any {
	projects, _ := m["projects"].(map[string]any)
	entry, _ := projects[dir].(map[string]any)
	return entry["hasTrustDialogAccepted"]
}

func TestEnsureTrusted_UntrustedPathGetsTrusted_OtherKeysPreserved(t *testing.T) {
	home := trustHome(t)
	work := filepath.Join(home, ".flow", "tasks", "x", "workspace")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	other := "/some/other/repo"
	p := writeClaudeJSON(t, home, `{
  "numStartups": 42,
  "oauthAccount": {"emailAddress": "a@b.c"},
  "projects": {
    "`+other+`": {"allowedTools": ["Bash"], "hasTrustDialogAccepted": false},
    "`+work+`": {"allowedTools": [], "lastCost": 1.5, "hasTrustDialogAccepted": false}
  }
}`)

	if err := EnsureTrusted(work); err != nil {
		t.Fatalf("EnsureTrusted: %v", err)
	}
	m := readClaudeJSON(t, p)
	if trusted(m, work) != true {
		t.Fatalf("work dir not trusted: %v", m["projects"])
	}
	if m["numStartups"].(float64) != 42 {
		t.Errorf("numStartups lost: %v", m["numStartups"])
	}
	if acct, _ := m["oauthAccount"].(map[string]any); acct["emailAddress"] != "a@b.c" {
		t.Errorf("oauthAccount lost: %v", m["oauthAccount"])
	}
	projects := m["projects"].(map[string]any)
	if trusted(m, other) != false {
		t.Errorf("unrelated project's trust changed: %v", projects[other])
	}
	if tools := projects[other].(map[string]any)["allowedTools"].([]any); len(tools) != 1 {
		t.Errorf("unrelated project's keys lost: %v", projects[other])
	}
	if projects[work].(map[string]any)["lastCost"].(float64) != 1.5 {
		t.Errorf("work entry's other keys lost: %v", projects[work])
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("file mode changed to %v, want 0600", st.Mode().Perm())
	}
}

func TestEnsureTrusted_NewProjectEntryCreated(t *testing.T) {
	home := trustHome(t)
	work := filepath.Join(home, "repo")
	p := writeClaudeJSON(t, home, `{"numStartups": 1}`)
	if err := EnsureTrusted(work); err != nil {
		t.Fatalf("EnsureTrusted: %v", err)
	}
	if trusted(readClaudeJSON(t, p), work) != true {
		t.Fatal("new entry not created as trusted")
	}
}

func TestEnsureTrusted_AlreadyTrustedIsUntouched(t *testing.T) {
	home := trustHome(t)
	work := filepath.Join(home, "repo")
	body := `{"projects":{"` + work + `":{"hasTrustDialogAccepted":true}},   "weird":   1}`
	p := writeClaudeJSON(t, home, body)
	before, _ := os.Stat(p)

	if err := EnsureTrusted(work); err != nil {
		t.Fatalf("EnsureTrusted: %v", err)
	}
	raw, _ := os.ReadFile(p)
	if string(raw) != body {
		t.Fatalf("already-trusted file was rewritten:\n%s", raw)
	}
	after, _ := os.Stat(p)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("mtime changed on no-op")
	}
}

func TestEnsureTrusted_TrustedAncestorIsNoOp(t *testing.T) {
	home := trustHome(t)
	parent := filepath.Join(home, "code")
	body := `{"projects":{"` + parent + `":{"hasTrustDialogAccepted":true}}}`
	p := writeClaudeJSON(t, home, body)
	if err := EnsureTrusted(filepath.Join(parent, "sub", "repo")); err != nil {
		t.Fatalf("EnsureTrusted: %v", err)
	}
	if raw, _ := os.ReadFile(p); string(raw) != body {
		t.Fatalf("file rewritten although an ancestor is trusted:\n%s", raw)
	}
}

func TestEnsureTrusted_NeverBroadens(t *testing.T) {
	home := trustHome(t)
	p := writeClaudeJSON(t, home, `{"projects":{}}`)
	work := filepath.Join(home, ".flow", "tasks", "x", "workspace")

	if err := EnsureTrusted(work); err != nil {
		t.Fatalf("EnsureTrusted: %v", err)
	}
	m := readClaudeJSON(t, p)
	projects := m["projects"].(map[string]any)
	if len(projects) != 1 {
		t.Fatalf("expected exactly one trusted entry (the work dir), got %v", projects)
	}
	for _, bad := range []string{home, filepath.Dir(work), filepath.Join(home, ".flow"), "/"} {
		if _, ok := projects[bad]; ok {
			t.Errorf("trust broadened to %s", bad)
		}
	}
}

func TestEnsureTrusted_RefusesHomeRootAndRelative(t *testing.T) {
	home := trustHome(t)
	body := `{"projects":{}}`
	p := writeClaudeJSON(t, home, body)
	for _, dir := range []string{home, home + "/", "/", filepath.Dir(home), "relative/dir", ""} {
		if err := EnsureTrusted(dir); err == nil {
			t.Errorf("EnsureTrusted(%q) = nil, want refusal", dir)
		}
	}
	if raw, _ := os.ReadFile(p); string(raw) != body {
		t.Fatalf("refused paths still modified the file:\n%s", raw)
	}
}

func TestEnsureTrusted_MissingFileIsNoOp(t *testing.T) {
	home := trustHome(t)
	if err := EnsureTrusted(filepath.Join(home, "repo")); err != nil {
		t.Fatalf("missing ~/.claude.json should be a no-op, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatal("must not create ~/.claude.json from scratch")
	}
}

func TestEnsureTrusted_MalformedFileErrorsAndIsLeftAlone(t *testing.T) {
	home := trustHome(t)
	body := `{"projects": {not json`
	p := writeClaudeJSON(t, home, body)
	err := EnsureTrusted(filepath.Join(home, "repo"))
	if err == nil {
		t.Fatal("want error on malformed ~/.claude.json")
	}
	if !strings.Contains(err.Error(), ".claude.json") {
		t.Errorf("error should name the file: %v", err)
	}
	if raw, _ := os.ReadFile(p); string(raw) != body {
		t.Fatalf("malformed file was clobbered:\n%s", raw)
	}
}

func TestEnsureTrusted_ProjectsNotAnObjectErrors(t *testing.T) {
	home := trustHome(t)
	body := `{"projects": []}`
	p := writeClaudeJSON(t, home, body)
	if err := EnsureTrusted(filepath.Join(home, "repo")); err == nil {
		t.Fatal("want error when projects is not an object")
	}
	if raw, _ := os.ReadFile(p); string(raw) != body {
		t.Fatalf("file clobbered:\n%s", raw)
	}
}

func TestEnsureTrusted_HonorsClaudeConfigDir(t *testing.T) {
	home := trustHome(t)
	cfg := filepath.Join(home, "alt-config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	p := filepath.Join(cfg, ".claude.json")
	if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(home, "repo")
	if err := EnsureTrusted(work); err != nil {
		t.Fatal(err)
	}
	if trusted(readClaudeJSON(t, p), work) != true {
		t.Fatal("CLAUDE_CONFIG_DIR/.claude.json not updated")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatal("wrote ~/.claude.json despite CLAUDE_CONFIG_DIR")
	}
}

func TestEnsureTrusted_NoLeftoverTempFiles(t *testing.T) {
	home := trustHome(t)
	writeClaudeJSON(t, home, `{}`)
	if err := EnsureTrusted(filepath.Join(home, "repo")); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(home)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

// The --bg launch paths must trust the work dir BEFORE claude starts —
// a `claude --bg` session in an untrusted folder stalls silently on the
// trust gate with nobody there to accept it.
func TestSpawnBackground_TrustsWorkDirFirst(t *testing.T) {
	home := trustHome(t)
	p := writeClaudeJSON(t, home, `{}`)
	work := filepath.Join(home, ".flow", "tasks", "peer", "workspace")

	var trustedAtLaunch any
	orig := BGCommandRunner
	t.Cleanup(func() { BGCommandRunner = orig })
	BGCommandRunner = func(dir string, args []string) ([]byte, error) {
		if args[0] == "agents" {
			return []byte(realAgentsJSON), nil
		}
		trustedAtLaunch = trusted(readClaudeJSON(t, p), work)
		return []byte(realBanner), nil
	}
	c := &claude{}
	if _, err := c.SpawnBackground(work, "n", "hi", harness.LaunchOpts{}); err != nil {
		t.Fatal(err)
	}
	if trustedAtLaunch != true {
		t.Fatal("SpawnBackground launched claude before trusting the work dir")
	}

	trustedAtLaunch = nil
	writeClaudeJSON(t, home, `{}`)
	if _, err := c.ResumeBackground(work, "48d287d9-1ef0-4738-84b9-3110beb988c4", harness.LaunchOpts{}); err != nil {
		t.Fatal(err)
	}
	if trustedAtLaunch != true {
		t.Fatal("ResumeBackground launched claude before trusting the work dir")
	}
}

// If trust can't be ensured, fail loudly instead of launching a session
// that will silently stall.
func TestSpawnBackground_TrustFailureIsAClearError(t *testing.T) {
	home := trustHome(t)
	writeClaudeJSON(t, home, `{not json`)
	launched := false
	orig := BGCommandRunner
	t.Cleanup(func() { BGCommandRunner = orig })
	BGCommandRunner = func(dir string, args []string) ([]byte, error) {
		launched = true
		return []byte(realBanner), nil
	}
	_, err := (&claude{}).SpawnBackground(filepath.Join(home, "repo"), "n", "hi", harness.LaunchOpts{})
	if err == nil || !strings.Contains(err.Error(), "trust") {
		t.Fatalf("want a trust error, got %v", err)
	}
	if launched {
		t.Fatal("claude was launched despite the trust failure")
	}
}

func TestPrepareWorkDir_TrustsForHeadlessRuns(t *testing.T) {
	home := trustHome(t)
	p := writeClaudeJSON(t, home, `{}`)
	work := filepath.Join(home, "repo")
	prep, ok := New().(harness.WorkDirPreparer)
	if !ok {
		t.Fatal("claude harness must implement harness.WorkDirPreparer")
	}
	if err := prep.PrepareWorkDir(work); err != nil {
		t.Fatal(err)
	}
	if trusted(readClaudeJSON(t, p), work) != true {
		t.Fatal("PrepareWorkDir did not trust the work dir")
	}
}

// TestMain points HOME at a throwaway dir for the whole package so no
// test path that reaches EnsureTrusted (e.g. existing SpawnBackground
// tests) can ever touch the developer's real ~/.claude.json.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "flow-claude-test-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// Claude Code keys projects by the process cwd, which the OS reports with
// symlinks resolved (macOS: /var → /private/var). Trust both spellings so
// the dialog is skipped whichever one Claude sees.
func TestEnsureTrusted_AlsoTrustsResolvedSymlinkPath(t *testing.T) {
	home := trustHome(t)
	p := writeClaudeJSON(t, home, `{}`)
	real := filepath.Join(home, "real-repo")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "link-repo")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := EnsureTrusted(link); err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(link)
	m := readClaudeJSON(t, p)
	if trusted(m, link) != true || trusted(m, resolved) != true {
		t.Fatalf("want both %s and %s trusted, got %v", link, resolved, m["projects"])
	}
}
