package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/harness"
)

// Headless runs (auto runs, the close-out sweep, owner ticks) have no
// human to approve a fresh hook install, so they bypass Codex's hook
// trust — hooks then fire in the background the way Claude's do.
func TestAutoRunArgvBypassesHookTrust(t *testing.T) {
	argv := New().AutoRunArgv(testThreadID, "work", harness.LaunchOpts{SkipPermissions: true})
	want := []string{"codex", "exec", "resume", "--dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-hook-trust", testThreadID, "work"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("AutoRunArgv=%q, want %q", argv, want)
	}
	orig := PSRunner
	t.Cleanup(func() { PSRunner = orig })
	PSRunner = func() ([]byte, error) { return []byte("400 " + strings.Join(argv, " ")), nil }
	live, err := New().LiveSessionIDs()
	if err != nil {
		t.Fatal(err)
	}
	if live[testThreadID] != 1 {
		t.Errorf("live=%v, want the hook-trust auto run detected", live)
	}
}

func TestSkipPermissionsRunArgvBypassesHookTrust(t *testing.T) {
	got := skipPermissionsArgv("sweep")
	want := []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-hook-trust", "sweep"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("skipPermissionsArgv=%q, want %q", got, want)
	}
}

// Interactive launches keep Codex's trust prompt: a human is at the TUI
// and approves a new hook once (the trust persists).
func TestInteractiveLaunchKeepsHookTrust(t *testing.T) {
	h := New()
	for _, cmd := range []string{
		h.LaunchCmd(testThreadID, "work", harness.LaunchOpts{}),
		h.LaunchCmd(testThreadID, "work", harness.LaunchOpts{SkipPermissions: true}),
		h.ResumeCmd(testThreadID, harness.LaunchOpts{}),
		h.ResumeCmd(testThreadID, harness.LaunchOpts{SkipPermissions: true}),
	} {
		if strings.Contains(cmd, "hook-trust") {
			t.Errorf("interactive command bypasses hook trust: %q", cmd)
		}
	}
}

// Regular (non-skip-permissions) interactive Codex sessions run in
// Codex's workspace-write sandbox with on-request approvals; flow's own
// bus/read commands escape it through the flow-owned exec-policy rules
// file (see flowRules). Skip-permissions keeps the full bypass. Neither
// --add-dir CODEX_HOME (lets the sandbox rewrite its own config) nor
// --add-dir FLOW_ROOT (unneeded with the rules file) may appear.
func TestInteractiveCommandsSandboxedUnlessSkip(t *testing.T) {
	h := New()
	const sandboxed = "codex -s workspace-write -a on-request resume "
	const bypass = "codex --dangerously-bypass-approvals-and-sandbox resume "
	for name, tc := range map[string]struct {
		cmd, prefix string
	}{
		"LaunchCmd regular":   {h.LaunchCmd(testThreadID, "work", harness.LaunchOpts{}), sandboxed},
		"LaunchCmd skip":      {h.LaunchCmd(testThreadID, "work", harness.LaunchOpts{SkipPermissions: true}), bypass},
		"ResumeCmd regular":   {h.ResumeCmd(testThreadID, harness.LaunchOpts{}), sandboxed},
		"ResumeCmd skip":      {h.ResumeCmd(testThreadID, harness.LaunchOpts{SkipPermissions: true}), bypass},
		"ResumeCmd injection": {h.ResumeCmd(testThreadID, harness.LaunchOpts{Inject: "follow up"}), sandboxed},
	} {
		if !strings.HasPrefix(tc.cmd, tc.prefix) {
			t.Errorf("%s = %q, want prefix %q", name, tc.cmd, tc.prefix)
		}
		if tc.prefix == sandboxed && strings.Contains(tc.cmd, "--dangerously-bypass-approvals-and-sandbox") {
			t.Errorf("%s: regular session must not bypass the sandbox: %q", name, tc.cmd)
		}
		if strings.Contains(tc.cmd, "--add-dir") {
			t.Errorf("%s: must not widen the sandbox with --add-dir: %q", name, tc.cmd)
		}
	}
	if got, want := h.ResumeCmd(testThreadID, harness.LaunchOpts{}), sandboxed+testThreadID; got != want {
		t.Errorf("ResumeCmd=%q, want %q", got, want)
	}
}

func readHooks(t *testing.T, path string) map[string][]map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return doc.Hooks
}

// Codex 0.166 fires UserPromptSubmit, PreToolUse, PostToolUse and Stop;
// each installs like SessionStart (idempotent, other entries preserved,
// no matcher — Codex tool names differ from Claude's, and the handlers'
// fast paths make an all-tools match cheap).
func TestInstallAllHookEvents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	path := filepath.Join(home, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	other := `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"other-tool hook"}]}]},"keep":1}`
	if err := os.WriteFile(path, []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	h := New()
	cases := []struct {
		event     string
		install   func(string) (bool, error)
		uninstall func(string) (bool, error)
	}{
		{"UserPromptSubmit", h.InstallUserPromptSubmitHook, h.UninstallUserPromptSubmitHook},
		{"PreToolUse", h.InstallPreToolUseHook, h.UninstallPreToolUseHook},
		{"PostToolUse", h.InstallPostToolUseHook, h.UninstallPostToolUseHook},
		{"Stop", h.InstallStopHook, h.UninstallStopHook},
	}
	for _, c := range cases {
		cmd := "flow hook " + strings.ToLower(c.event)
		if added, err := c.install(cmd); err != nil || !added {
			t.Fatalf("%s install added=%v err=%v", c.event, added, err)
		}
		if added, err := c.install(cmd); err != nil || added {
			t.Fatalf("%s idempotent install added=%v err=%v", c.event, added, err)
		}
		entries := readHooks(t, path)[c.event]
		found := false
		for _, e := range entries {
			inner, _ := e["hooks"].([]any)
			for _, hk := range inner {
				if m, _ := hk.(map[string]any); m["command"] == cmd {
					found = true
					if _, ok := e["matcher"]; ok {
						t.Errorf("%s entry has a matcher: %v", c.event, e)
					}
				}
			}
		}
		if !found {
			t.Errorf("%s hook %q not installed: %v", c.event, cmd, entries)
		}
	}
	if got := readHooks(t, path)["UserPromptSubmit"]; len(got) != 2 {
		t.Errorf("UserPromptSubmit entries = %v, want other-tool + flow", got)
	}
	for _, c := range cases {
		cmd := "flow hook " + strings.ToLower(c.event)
		if removed, err := c.uninstall(cmd); err != nil || !removed {
			t.Fatalf("%s uninstall removed=%v err=%v", c.event, removed, err)
		}
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "other-tool hook") || !strings.Contains(string(raw), `"keep"`) {
		t.Errorf("uninstall dropped foreign entries: %s", raw)
	}
}

// Codex reads hooks.json from $CODEX_HOME (default ~/.codex), the same
// root the transcript reader honors.
func TestHooksHonorCodexHome(t *testing.T) {
	home := t.TempDir()
	codexHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	if _, err := New().InstallSessionStartHook("flow hook session-start"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(codexHome, "hooks.json")); err != nil {
		t.Errorf("hooks.json not under $CODEX_HOME: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "hooks.json")); err == nil {
		t.Errorf("hooks.json written under ~/.codex despite $CODEX_HOME")
	}
}

// Codex loads skills from $CODEX_HOME/skills, so flow installs there too.
func TestSkillInstallPathHonorsCodexHome(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", codexHome)
	p, err := New().SkillInstallPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(codexHome, "skills", "flow", "SKILL.md"); p != want {
		t.Errorf("SkillInstallPath=%q, want %q", p, want)
	}
}
