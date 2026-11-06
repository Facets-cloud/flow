package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
	"flow/internal/harness/opencode"
)

// stubOpencodeAPI replaces opencode.APIRunner with a recorder that also
// honors a forced failure for session.create. Returns the captured call
// argument lists and the last session.create payload (decoded lazily by
// the caller).
func stubOpencodeAPI(t *testing.T, failCreate bool) *[][]string {
	t.Helper()
	var calls [][]string
	old := opencode.APIRunner
	opencode.APIRunner = func(args ...string) ([]byte, error) {
		copied := append([]string(nil), args...)
		calls = append(calls, copied)
		if failCreate && len(args) > 0 && args[0] == "session.create" {
			return nil, errors.New("simulated opencode server failure")
		}
		return []byte("{}"), nil
	}
	t.Cleanup(func() { opencode.APIRunner = old })
	return &calls
}

// TestPrepareHarnessSessionClaudeNoOp: claude does not implement
// SessionPreparer, so prepareHarnessSession is a no-op and writes no
// opencode files.
func TestPrepareHarnessSessionClaudeNoOp(t *testing.T) {
	setupFlowRoot(t)
	task := &flowdb.Task{Slug: "claude-task", WorkDir: t.TempDir()}

	if err := prepareHarnessSession(claude.New(), task, "658bf2be-5ae3-4842-a8a4-e0d0b785514d", false); err != nil {
		t.Fatalf("prepareHarnessSession(claude) = %v, want nil", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	ocDir := filepath.Join(home, ".config", "opencode")
	if _, err := os.Stat(ocDir); !os.IsNotExist(err) {
		t.Errorf("claude prepare wrote %s (err=%v), want no opencode files", ocDir, err)
	}
}

// TestPrepareHarnessSessionOpencodeInstallsAndPrepares pins the opencode
// prepare contract: skill tree + VERSION sidecar + plugin written under
// $HOME/.config/opencode, and session.create called with flow metadata
// carrying the task slug, FLOW_ROOT, and the auto flag.
func TestPrepareHarnessSessionOpencodeInstallsAndPrepares(t *testing.T) {
	setupFlowRoot(t)
	// Pin a real version: a dev build reinstalls on every prepare (see
	// TestEnsureHarnessInstalledReinstallsSkillForDevBuild), which would
	// defeat the "matching VERSION skips install" assertion below.
	withVersion(t, "v9.9.9-test")
	calls := stubOpencodeAPI(t, false)

	const slug = "oc-task"
	const sid = "ses_0123456789ABCDEFGHIJKLMNOP"
	task := &flowdb.Task{Slug: slug, WorkDir: t.TempDir()}

	if err := prepareHarnessSession(opencode.New(), task, sid, true); err != nil {
		t.Fatalf("prepareHarnessSession = %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(home, ".config", "opencode", "skills", "flow", "SKILL.md")
	if b, err := os.ReadFile(skillPath); err != nil || len(b) == 0 {
		t.Fatalf("SKILL.md not installed at %s: %v", skillPath, err)
	}
	versionPath := filepath.Join(home, ".config", "opencode", "skills", "flow", "VERSION")
	if b, err := os.ReadFile(versionPath); err != nil || strings.TrimSpace(string(b)) != Version {
		t.Fatalf("VERSION = %q (err=%v), want %q", string(b), err, Version)
	}
	pluginPath := filepath.Join(home, ".config", "opencode", "plugins", "flow", "index.js")
	if b, err := os.ReadFile(pluginPath); err != nil || len(b) == 0 {
		t.Fatalf("plugin not installed at %s: %v", pluginPath, err)
	}

	var createPayload map[string]any
	for _, c := range *calls {
		if len(c) >= 3 && c[0] == "session.create" && c[1] == "-d" {
			if err := json.Unmarshal([]byte(c[2]), &createPayload); err != nil {
				t.Fatalf("decode session.create payload: %v", err)
			}
		}
	}
	if createPayload == nil {
		t.Fatalf("APIRunner never got session.create; calls=%v", *calls)
	}
	if got := createPayload["id"]; got != sid {
		t.Errorf("session.create id = %v, want %s", got, sid)
	}
	meta, _ := createPayload["metadata"].(map[string]any)
	flow, _ := meta["flow"].(map[string]any)
	if got := flow["taskSlug"]; got != slug {
		t.Errorf("metadata.flow.taskSlug = %v, want %s", got, slug)
	}
	if got := flow["auto"]; got != true {
		t.Errorf("metadata.flow.auto = %v, want true", got)
	}
	if got := flow["flowRoot"]; got != os.Getenv("FLOW_ROOT") {
		t.Errorf("metadata.flow.flowRoot = %v, want %q", got, os.Getenv("FLOW_ROOT"))
	}

	// A second call with a matching VERSION must not rewrite the skill.
	if err := os.WriteFile(skillPath, []byte("sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareHarnessSession(opencode.New(), task, sid, false); err != nil {
		t.Fatalf("second prepareHarnessSession = %v", err)
	}
	if b, _ := os.ReadFile(skillPath); string(b) != "sentinel" {
		t.Errorf("second prepare rewrote SKILL.md (got %q); VERSION match should skip install", string(b))
	}
}

// TestPrepareHarnessSessionNoopForNonPreparer guards the capability
// boundary the no-op branch relies on.
func TestPrepareHarnessSessionNoopForNonPreparer(t *testing.T) {
	// Compile-time guard: claude must NOT implement SessionPreparer.
	if _, ok := claude.New().(harness.SessionPreparer); ok {
		t.Fatal("claude implements SessionPreparer; the no-op branch would never run")
	}
	if _, ok := opencode.New().(harness.SessionPreparer); !ok {
		t.Fatal("opencode does not implement SessionPreparer")
	}
}

// TestEnsureHarnessInstalledReinstallsSkillForDevBuild: a dev build's
// version string never changes, so the VERSION sidecar always matches
// after the first install. The skill must still be reinstalled on every
// prepare — otherwise edits to the embedded skill never reach installs
// made by a dev binary.
func TestEnsureHarnessInstalledReinstallsSkillForDevBuild(t *testing.T) {
	setupFlowRoot(t)
	withVersion(t, "dev")
	stubOpencodeAPI(t, false)

	h := opencode.New()
	if err := ensureHarnessInstalled(h); err != nil {
		t.Fatalf("first ensureHarnessInstalled = %v", err)
	}
	skillPath, err := h.SkillInstallPath()
	if err != nil {
		t.Fatal(err)
	}
	// The sidecar now matches "dev" exactly — the frozen state the dev
	// reinstall rule exists for.
	versionPath, err := h.SkillVersionPath()
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(versionPath); err != nil || strings.TrimSpace(string(b)) != "dev" {
		t.Fatalf("VERSION = %q (err=%v), want dev", string(b), err)
	}
	if err := os.WriteFile(skillPath, []byte("sentinel"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureHarnessInstalled(h); err != nil {
		t.Fatalf("second ensureHarnessInstalled = %v", err)
	}
	if b, _ := os.ReadFile(skillPath); string(b) == "sentinel" {
		t.Error("dev build skipped the skill install although VERSION matched; edits would freeze")
	}
}

// clearHarnessSessionEnv unsets every harness's session-id env var so
// ambientHarness deterministically falls back to claude in app tests.
func clearHarnessSessionEnv(t *testing.T) {
	t.Helper()
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}
}

// apiPayload returns the decoded `-d` body of the last APIRunner call
// matching op (optionally filtered to the call carrying param=value).
func apiPayload(t *testing.T, calls [][]string, op, param, value string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, c := range calls {
		if len(c) < 3 || c[0] != op {
			continue
		}
		if param != "" {
			hasParam := false
			for i := 1; i+1 < len(c); i++ {
				if c[i] == "--param" && c[i+1] == param+"="+value {
					hasParam = true
					break
				}
			}
			if !hasParam {
				continue
			}
		}
		body := ""
		for i := 1; i+1 < len(c); i++ {
			if c[i] == "-d" {
				body = c[i+1]
				break
			}
		}
		if body == "" {
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			t.Fatalf("decode %s payload: %v", op, err)
		}
		found = decoded
	}
	return found
}

// TestCmdDoHereOpencodePreparesSession pins that binding an opencode
// session with `--here` immediately writes flow metadata server-side, so
// the flow plugin activates on this session instead of ignoring it. The
// metadata must carry the task slug on both session.create and
// session.update (the latter is what a pre-existing session needs).
func TestCmdDoHereOpencodePreparesSession(t *testing.T) {
	setupFlowRoot(t)
	seedTaskAtCwd(t, "oc-here-task")
	clearHarnessSessionEnv(t)

	const sid = "ses_0123456789ABCDEFGHIJKLMNOP"
	t.Setenv("OPENCODE_SESSION_ID", sid)
	calls := stubOpencodeAPI(t, false)

	if rc := cmdDo([]string{"oc-here-task", "--here"}); rc != 0 {
		t.Fatalf("--here rc=%d, want 0", rc)
	}

	create := apiPayload(t, *calls, "session.create", "", "")
	if create == nil {
		t.Fatalf("APIRunner never got session.create; calls=%v", *calls)
	}
	createMeta, _ := create["metadata"].(map[string]any)
	flow, _ := createMeta["flow"].(map[string]any)
	if got := flow["taskSlug"]; got != "oc-here-task" {
		t.Errorf("session.create metadata.flow.taskSlug = %v, want oc-here-task", got)
	}

	update := apiPayload(t, *calls, "session.update", "sessionID", sid)
	if update == nil {
		t.Fatalf("APIRunner never got session.update for %s; calls=%v", sid, *calls)
	}
	updateMeta, _ := update["metadata"].(map[string]any)
	flow, _ = updateMeta["flow"].(map[string]any)
	if got := flow["taskSlug"]; got != "oc-here-task" {
		t.Errorf("session.update metadata.flow.taskSlug = %v, want oc-here-task", got)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "oc-here-task")
	if err != nil {
		t.Fatal(err)
	}
	if !task.SessionID.Valid || task.SessionID.String != sid {
		t.Errorf("session_id = %+v, want %s", task.SessionID, sid)
	}
	if !task.Harness.Valid || task.Harness.String != string(harness.NameOpencode) {
		t.Errorf("task.harness = %+v, want opencode", task.Harness)
	}
}

// TestCmdDoHereOpencodePrepareFailureKeepsBind pins the warning-not-rollback
// rule: the bind is already committed, so a failed session prepare prints a
// warning and still exits 0 with the binding intact.
func TestCmdDoHereOpencodePrepareFailureKeepsBind(t *testing.T) {
	setupFlowRoot(t)
	seedTaskAtCwd(t, "oc-here-fail")
	clearHarnessSessionEnv(t)

	const sid = "ses_0123456789ABCDEFGHIJKLMNOP"
	t.Setenv("OPENCODE_SESSION_ID", sid)
	stubOpencodeAPI(t, true)
	drain := captureStderr(t)

	rc := cmdDo([]string{"oc-here-fail", "--here"})
	stderr := drain()
	if rc != 0 {
		t.Errorf("--here rc=%d, want 0 (bind succeeded; prepare failure is a warning)", rc)
	}
	if !strings.Contains(stderr, "warning: prepare opencode session:") {
		t.Errorf("stderr missing prepare warning:\n%s", stderr)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "oc-here-fail")
	if err != nil {
		t.Fatal(err)
	}
	if !task.SessionID.Valid || task.SessionID.String != sid {
		t.Errorf("session_id = %+v, want %s (binding must survive a prepare failure)", task.SessionID, sid)
	}
	if task.Status != "in-progress" {
		t.Errorf("status = %q, want in-progress", task.Status)
	}
}

// TestCmdDoHereClaudeMakesNoOpencodeAPICall pins the harness boundary:
// claude does not implement SessionPreparer, so a claude `--here` bind
// must never shell out to `opencode api`.
func TestCmdDoHereClaudeMakesNoOpencodeAPICall(t *testing.T) {
	setupFlowRoot(t)
	seedTaskAtCwd(t, "claude-here-task")
	clearHarnessSessionEnv(t)
	stubClaudeStatOK(t)

	const sid = "f00ba111-2222-4333-8444-555555555555"
	t.Setenv("CLAUDE_CODE_SESSION_ID", sid)
	calls := stubOpencodeAPI(t, false)

	if rc := cmdDo([]string{"claude-here-task", "--here"}); rc != 0 {
		t.Fatalf("claude --here rc=%d, want 0", rc)
	}
	if len(*calls) != 0 {
		t.Errorf("claude --here called the opencode API: %v", *calls)
	}
}
