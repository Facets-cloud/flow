package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// harnessEnv is a hermetic two-harness sandbox: a temp $HOME (claude's
// config home is $HOME/.claude) and a separate temp $CODEX_HOME, plus a
// stubbed binary lookup so "present on PATH" is under test control.
type harnessEnv struct {
	home      string
	codexHome string
}

func (e harnessEnv) claudeSkill() string {
	return filepath.Join(e.home, ".claude", "skills", "flow", "SKILL.md")
}
func (e harnessEnv) codexSkill() string {
	return filepath.Join(e.codexHome, "skills", "flow", "SKILL.md")
}
func (e harnessEnv) claudeSettings() string { return filepath.Join(e.home, ".claude", "settings.json") }
func (e harnessEnv) codexHooks() string     { return filepath.Join(e.codexHome, "hooks.json") }

// withHarnessEnv sets up the sandbox. onPath names the harness binaries
// the stubbed lookup reports as installed; every other name is "not
// found". CODEX_HOME points at a path that does NOT exist yet, so codex
// is only present when its binary is "on PATH" or the test creates the dir.
func withHarnessEnv(t *testing.T, onPath ...string) harnessEnv {
	t.Helper()
	home := t.TempDir()
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "")
	found := map[string]bool{}
	for _, b := range onPath {
		found[b] = true
	}
	old := harnessLookPath
	harnessLookPath = func(bin string) (string, error) {
		if found[bin] {
			return "/fake/bin/" + bin, nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { harnessLookPath = old })
	return harnessEnv{home: home, codexHome: codexHome}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// assertAllFlowHooks checks the four live flow hooks are registered in
// the given settings/hooks file.
func assertAllFlowHooks(t *testing.T, path string) {
	t.Helper()
	settings := readSettings(t, path)
	hooks, _ := settings["hooks"].(map[string]any)
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "Stop"} {
		if !hookEventReferencesCommand(hooks, ev, expectedCommand(ev)) {
			t.Errorf("%s: %s hook missing; hooks=%#v", path, ev, hooks[ev])
		}
	}
}

func TestSkillInstallAllPresentHarnesses(t *testing.T) {
	e := withHarnessEnv(t, "claude", "codex")
	var rc int
	out := captureStdout(t, func() { rc = cmdSkill([]string{"install"}) })
	if rc != 0 {
		t.Fatalf("install rc=%d\n%s", rc, out)
	}
	for _, p := range []string{e.claudeSkill(), e.codexSkill()} {
		if !fileExists(p) {
			t.Errorf("missing %s", p)
		}
	}
	assertAllFlowHooks(t, e.claudeSettings())
	assertAllFlowHooks(t, e.codexHooks())
	if !strings.Contains(out, "claude: installed flow skill") || !strings.Contains(out, "codex: installed flow skill") {
		t.Errorf("want per-harness lines, got:\n%s", out)
	}
}

func TestSkillInstallOnlyClaudePresent(t *testing.T) {
	e := withHarnessEnv(t, "claude")
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	if !fileExists(e.claudeSkill()) {
		t.Error("claude skill missing")
	}
	if fileExists(e.codexSkill()) || fileExists(e.codexHooks()) {
		t.Error("codex not present but got installed")
	}
}

// Codex counts as present when only its config home exists (no binary).
func TestSkillInstallCodexPresentByConfigDir(t *testing.T) {
	e := withHarnessEnv(t)
	if err := os.MkdirAll(e.codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	if !fileExists(e.codexSkill()) {
		t.Error("codex config home exists → codex should be installed")
	}
	if fileExists(e.claudeSkill()) {
		t.Error("claude not present (no binary, no ~/.claude) but got installed")
	}
	assertAllFlowHooks(t, e.codexHooks())
}

// Nothing detected → fall back to the default harness (claude), the
// historical behavior, so a fresh machine still gets a working install.
func TestSkillInstallNothingPresentFallsBackToDefault(t *testing.T) {
	e := withHarnessEnv(t)
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	if !fileExists(e.claudeSkill()) {
		t.Error("fallback should install for claude")
	}
	if fileExists(e.codexSkill()) {
		t.Error("codex should not be installed in fallback")
	}
}

func TestSkillInstallHarnessFlagLimits(t *testing.T) {
	e := withHarnessEnv(t, "claude", "codex")
	if rc := cmdSkill([]string{"install", "--harness", "codex"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	if !fileExists(e.codexSkill()) {
		t.Error("codex skill missing")
	}
	if fileExists(e.claudeSkill()) || fileExists(e.claudeSettings()) {
		t.Error("--harness codex must not touch claude")
	}
}

// An explicitly named harness installs even when not detected: the user
// asked for it (e.g. provisioning before installing the binary).
func TestSkillInstallHarnessFlagNotPresentInstallsAnyway(t *testing.T) {
	e := withHarnessEnv(t, "claude")
	if rc := cmdSkill([]string{"install", "--harness", "codex"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	if !fileExists(e.codexSkill()) {
		t.Error("explicit --harness codex should install even if not detected")
	}
	assertAllFlowHooks(t, e.codexHooks())
}

func TestSkillHarnessFlagUnknownIsUsageError(t *testing.T) {
	withHarnessEnv(t, "claude")
	for _, sub := range []string{"install", "update", "uninstall"} {
		var rc int
		out := captureStdout(t, func() { rc = cmdSkill([]string{sub, "--harness", "bogus"}) })
		if rc != 2 {
			t.Errorf("%s --harness bogus rc=%d, want 2", sub, rc)
		}
		if !strings.Contains(out, "claude") || !strings.Contains(out, "codex") {
			t.Errorf("%s: error should list valid names, got:\n%s", sub, out)
		}
	}
	var rc int
	out := captureStdout(t, func() { rc = cmdInit([]string{"--harness", "bogus"}) })
	if rc != 2 {
		t.Errorf("init --harness bogus rc=%d, want 2\n%s", rc, out)
	}
}

func TestSkillUpdateAllPresentAndHarnessFlag(t *testing.T) {
	e := withHarnessEnv(t, "claude", "codex")
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatal("install")
	}
	for _, p := range []string{e.claudeSkill(), e.codexSkill()} {
		if err := os.WriteFile(p, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if rc := cmdSkill([]string{"update", "--harness", "codex"}); rc != 0 {
		t.Fatal("update --harness codex")
	}
	if b, _ := os.ReadFile(e.codexSkill()); string(b) == "stale" {
		t.Error("codex not refreshed")
	}
	if b, _ := os.ReadFile(e.claudeSkill()); string(b) != "stale" {
		t.Error("--harness codex must leave claude alone")
	}
	if rc := cmdSkill([]string{"update"}); rc != 0 {
		t.Fatal("update")
	}
	if b, _ := os.ReadFile(e.claudeSkill()); string(b) == "stale" {
		t.Error("update should refresh every present harness")
	}
}

func TestSkillUninstallAllInstalled(t *testing.T) {
	e := withHarnessEnv(t, "claude", "codex")
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatal("install")
	}
	if rc := cmdSkill([]string{"uninstall"}); rc != 0 {
		t.Fatal("uninstall")
	}
	if fileExists(filepath.Dir(e.claudeSkill())) || fileExists(filepath.Dir(e.codexSkill())) {
		t.Error("uninstall should remove the skill from every harness")
	}
	for _, p := range []string{e.claudeSettings(), e.codexHooks()} {
		hooks, _ := readSettings(t, p)["hooks"].(map[string]any)
		if len(hooks) != 0 {
			t.Errorf("%s: hooks left after uninstall: %#v", p, hooks)
		}
	}
}

func TestSkillUninstallHarnessFlagLimits(t *testing.T) {
	e := withHarnessEnv(t, "claude", "codex")
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatal("install")
	}
	if rc := cmdSkill([]string{"uninstall", "--harness", "claude"}); rc != 0 {
		t.Fatal("uninstall --harness claude")
	}
	if fileExists(e.claudeSkill()) {
		t.Error("claude skill should be gone")
	}
	if !fileExists(e.codexSkill()) {
		t.Error("--harness claude must leave codex installed")
	}
	assertAllFlowHooks(t, e.codexHooks())
}

func TestCmdInitInstallsAllPresentHarnesses(t *testing.T) {
	initTempFlowRoot(t)
	e := withHarnessEnv(t, "claude", "codex")
	if rc := cmdInit(nil); rc != 0 {
		t.Fatalf("init rc=%d", rc)
	}
	for _, p := range []string{e.claudeSkill(), e.codexSkill()} {
		if !fileExists(p) {
			t.Errorf("missing %s", p)
		}
	}
	assertAllFlowHooks(t, e.claudeSettings())
	assertAllFlowHooks(t, e.codexHooks())
}

func TestCmdInitHarnessFlagLimits(t *testing.T) {
	initTempFlowRoot(t)
	e := withHarnessEnv(t, "claude", "codex")
	if rc := cmdInit([]string{"--harness", "codex"}); rc != 0 {
		t.Fatalf("init rc=%d", rc)
	}
	if !fileExists(e.codexSkill()) || fileExists(e.claudeSkill()) {
		t.Error("init --harness codex should install codex only")
	}
}

func TestMaybeAutoUpgradeRefreshesEveryInstalledHarness(t *testing.T) {
	e := withHarnessEnv(t, "claude", "codex")
	withVersion(t, "v1.0.0")
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatal("install")
	}
	for _, p := range []string{e.claudeSkill(), e.codexSkill()} {
		if err := os.WriteFile(p, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	Version = "v2.0.0"
	maybeAutoUpgradeSkill()
	for _, p := range []string{e.claudeSkill(), e.codexSkill()} {
		if b, _ := os.ReadFile(p); string(b) == "stale" {
			t.Errorf("%s not refreshed", p)
		}
		v, _ := os.ReadFile(filepath.Join(filepath.Dir(p), "VERSION"))
		if strings.TrimSpace(string(v)) != "v2.0.0" {
			t.Errorf("%s sidecar=%q, want v2.0.0", p, v)
		}
	}
}

// Skill missing in one harness = that harness opted out; never
// reinstall it behind the user's back, but still refresh the other.
func TestMaybeAutoUpgradeSkipsHarnessWithoutSkill(t *testing.T) {
	e := withHarnessEnv(t, "claude", "codex")
	withVersion(t, "v1.0.0")
	if rc := cmdSkill([]string{"install", "--harness", "claude"}); rc != 0 {
		t.Fatal("install")
	}
	if err := os.WriteFile(e.claudeSkill(), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	Version = "v2.0.0"
	maybeAutoUpgradeSkill()
	if b, _ := os.ReadFile(e.claudeSkill()); string(b) == "stale" {
		t.Error("claude not refreshed")
	}
	if fileExists(filepath.Dir(e.codexSkill())) || fileExists(e.codexHooks()) {
		t.Error("auto-upgrade installed codex, which never had the skill")
	}
}

// Each harness's sidecar is read separately: a harness already at the
// running version is left alone while a stale one is refreshed.
func TestMaybeAutoUpgradeReadsSidecarPerHarness(t *testing.T) {
	e := withHarnessEnv(t, "claude", "codex")
	withVersion(t, "v2.0.0")
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatal("install")
	}
	// claude is current; codex is stale at v1.
	if err := os.WriteFile(filepath.Join(filepath.Dir(e.codexSkill()), "VERSION"), []byte("v1.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.codexSkill(), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.claudeSkill(), []byte("claude-local"), 0o644); err != nil {
		t.Fatal(err)
	}
	maybeAutoUpgradeSkill()
	if b, _ := os.ReadFile(e.codexSkill()); string(b) == "stale" {
		t.Error("stale codex not refreshed")
	}
	if b, _ := os.ReadFile(e.claudeSkill()); string(b) != "claude-local" {
		t.Error("claude was current; must not be rewritten")
	}
}
