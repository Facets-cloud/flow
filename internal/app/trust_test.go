package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/harness/claude"
)

// claudeTrusted reports whether dir carries hasTrustDialogAccepted in the
// test $HOME's ~/.claude.json.
func claudeTrusted(t *testing.T, dir string) bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".claude.json"))
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Projects map[string]struct {
			Trusted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse claude.json: %v\n%s", err, b)
	}
	return m.Projects[dir].Trusted
}

func resolvedTasksDir(t *testing.T, root string) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestInitTrustsFlowTasksDirForClaude(t *testing.T) {
	root := initTempFlowRoot(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if rc := cmdInit([]string{"--harness", "claude"}); rc != 0 {
		t.Fatalf("cmdInit rc=%d", rc)
	}
	if !claudeTrusted(t, resolvedTasksDir(t, root)) {
		t.Fatal("flow init did not pre-trust $FLOW_ROOT/tasks for claude")
	}
}

func TestInitDoesNotTouchClaudeConfigForCodexOnly(t *testing.T) {
	initTempFlowRoot(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", t.TempDir())
	if rc := cmdInit([]string{"--harness", "codex"}); rc != 0 {
		t.Fatalf("cmdInit rc=%d", rc)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".claude.json")); !os.IsNotExist(err) {
		t.Fatal("codex-only init wrote ~/.claude.json")
	}
}

func TestInitMalformedClaudeConfigWarnsAndLeavesItAlone(t *testing.T) {
	initTempFlowRoot(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	cfg := filepath.Join(os.Getenv("HOME"), ".claude.json")
	os.WriteFile(cfg, []byte(`{"broken":`), 0o600)
	if rc := cmdInit([]string{"--harness", "claude"}); rc != 0 {
		t.Fatalf("init must not fail on an unparseable claude config, rc=%d", rc)
	}
	if b, _ := os.ReadFile(cfg); string(b) != `{"broken":` {
		t.Fatalf("malformed config modified: %s", b)
	}
}

// The reported bug: a bg (claude --bg) spawn in a flow-created workspace
// died with "Workspace not trusted". flow must pre-trust before spawning.
func TestCmdDoBackgroundPreTrustsFlowWorkspace(t *testing.T) {
	root := setupFlowRoot(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	seedTask(t, "bgtrust")
	// Simulate a config written before this feature (init no longer the
	// only path): wipe it so the spawn path itself must trust.
	os.Remove(filepath.Join(os.Getenv("HOME"), ".claude.json"))
	stubBGMode(t)
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	stubBGCommand(t, &reg)
	tasks := resolvedTasksDir(t, root)
	trustedAtSpawn := false
	recorder := claude.BGCommandRunner
	claude.BGCommandRunner = func(workDir string, args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "--bg" {
			trustedAtSpawn = claudeTrusted(t, tasks)
		}
		return recorder(workDir, args)
	}
	t.Cleanup(func() { claude.BGCommandRunner = recorder })

	if rc := cmdDo([]string{"bgtrust"}); rc != 0 {
		t.Fatalf("cmdDo rc=%d", rc)
	}
	if !trustedAtSpawn {
		t.Fatal("flow workspace was not trusted before claude --bg ran")
	}
}

func TestCmdDoBackgroundNeverTrustsUserRepo(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	plain, _ := filepath.EvalSymlinks(t.TempDir()) // not a repo, but not flow's either
	for slug, wd := range map[string]string{"inrepo": repo, "plain": plain} {
		if rc := cmdAdd([]string{"task", slug, "--work-dir", wd}); rc != 0 {
			t.Fatalf("add rc=%d", rc)
		}
	}
	stubBGMode(t)
	stubITerm(t)
	reg := bgAgentsJSON(bgFullSID)
	stubBGCommand(t, &reg)
	cfg := filepath.Join(os.Getenv("HOME"), ".claude.json")
	before, _ := os.ReadFile(cfg)

	if rc := cmdDo([]string{"inrepo"}); rc != 0 {
		t.Fatalf("cmdDo rc=%d", rc)
	}
	if after, _ := os.ReadFile(cfg); string(after) != string(before) {
		t.Fatalf("spawning in a user repo modified claude.json:\n%s", after)
	}
	if claudeTrusted(t, repo) || claudeTrusted(t, plain) {
		t.Fatal("user dir trusted")
	}
}

func TestCmdDoCodexTabCarriesTrustOnlyForFlowWorkspace(t *testing.T) {
	setupFlowRoot(t)
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}
	const sid = "019ff4b1-6162-7263-974f-f5f1866ad0fe"
	stubCodexProbe(t, sid)
	_, script := stubITerm(t)

	seedTask(t, "cx-flow")
	if rc := cmdDo([]string{"cx-flow", "--harness", "codex"}); rc != 0 {
		t.Fatalf("cmdDo rc=%d", rc)
	}
	if !strings.Contains(script(), "trust_level") || !strings.Contains(script(), "cx-flow/workspace") {
		t.Errorf("codex launch in flow workspace lacks trust override: %q", script())
	}

	repo := t.TempDir()
	if rc := cmdAdd([]string{"task", "cx-user", "--work-dir", repo}); rc != 0 {
		t.Fatal("add")
	}
	stubCodexProbe(t, "019ff4b1-6162-7263-974f-f5f1866ad0ff")
	if rc := cmdDo([]string{"cx-user", "--harness", "codex"}); rc != 0 {
		t.Fatalf("cmdDo rc=%d", rc)
	}
	if strings.Contains(script(), "trust_level") {
		t.Errorf("codex launch in a user dir carries trust: %q", script())
	}
}
