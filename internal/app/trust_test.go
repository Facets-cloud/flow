package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"flow/internal/flowdb"
)

// seedClaudeJSON writes a minimal Claude Code global config under the
// test's temp HOME (setupFlowRoot points HOME at a temp dir).
func seedClaudeJSON(t *testing.T, body string) string {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func claudeTrusted(t *testing.T, cfg, dir string) bool {
	t.Helper()
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Projects map[string]struct {
			Accepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	return m.Projects[dir].Accepted
}

// A flow-owned workspace (tasks/<slug>/workspace) is trusted the moment
// flow creates it, so any later headless/background Claude session there
// never stalls on the folder-trust dialog.
func TestAddTask_FlowWorkspaceIsTrustedForClaude(t *testing.T) {
	setupFlowRoot(t)
	cfg := seedClaudeJSON(t, `{"numStartups": 3}`)

	if rc := cmdAdd([]string{"task", "ws-trust"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "ws-trust")
	if err != nil {
		t.Fatal(err)
	}
	if !claudeTrusted(t, cfg, task.WorkDir) {
		t.Fatalf("flow-owned workspace %s not trusted", task.WorkDir)
	}
}

// An explicit, user-chosen work_dir is NOT trusted at add time — only at
// launch, and only for the harness that will run there.
func TestAddTask_ExplicitWorkDirNotTrustedAtAdd(t *testing.T) {
	setupFlowRoot(t)
	cfg := seedClaudeJSON(t, `{}`)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"task", "explicit", "--work-dir", wd}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	if claudeTrusted(t, cfg, wd) {
		t.Fatal("explicit work_dir should not be trusted at add time")
	}
}

// A broken ~/.claude.json must not make task creation fail — trust at
// add time is best-effort; the launch path reports the problem.
func TestAddTask_MalformedClaudeJSONDoesNotBlockAdd(t *testing.T) {
	setupFlowRoot(t)
	cfg := seedClaudeJSON(t, `{broken`)
	if rc := cmdAdd([]string{"task", "still-works"}); rc != 0 {
		t.Fatalf("add rc=%d, want 0", rc)
	}
	if raw, _ := os.ReadFile(cfg); string(raw) != `{broken` {
		t.Fatal("malformed config was modified")
	}
}

// `flow do --auto` trusts the task's work_dir before launching the
// detached run.
func TestDoAuto_TrustsWorkDirBeforeLaunch(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"task", "auto-trust", "--work-dir", wd}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	cfg := seedClaudeJSON(t, `{}`)

	var trustedAtLaunch bool
	old := autoLauncher
	t.Cleanup(func() { autoLauncher = old })
	autoLauncher = func(slug, workDir, logPath, injection string, env []string) (int, error) {
		trustedAtLaunch = claudeTrusted(t, cfg, workDir)
		return 777, nil
	}
	if rc := cmdDo([]string{"auto-trust", "--auto"}); rc != 0 {
		t.Fatalf("do --auto rc=%d", rc)
	}
	if !trustedAtLaunch {
		t.Fatal("work_dir was not trusted before the auto run launched")
	}
}
