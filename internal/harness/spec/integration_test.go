package spec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"flow/internal/harness"
)

func TestPromptPreludePreparesBootstrapAndResumePrompts(t *testing.T) {
	a := loadSpec(t, minimalManifest+`

[hooks]
strategies = ["prompt-prelude"]
`)
	hooks := a.Hooks()
	if hooks == nil {
		t.Fatal("prompt-prelude manifest has no hook capability")
	}
	if got, want := hooks.PreparePrompt("bootstrap", "session context"), "session context\n\nbootstrap"; got != want {
		t.Errorf("bootstrap prompt = %q, want %q", got, want)
	}
	if got, want := hooks.PreparePrompt("", "session context"), "session context"; got != want {
		t.Errorf("resume prompt = %q, want %q", got, want)
	}
}

func TestHeadlessRunReceivesWorkDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	manifest := minimalManifest + `

[headless]
run_argv = ["sh", "-c", "printf %s \"$1\" > \"$2\"", "_", "{{.WorkDir}}", "{{.Home}}/captured-workdir"]
`
	a := loadSpec(t, manifest)
	want := filepath.Join(home, "workspace with spaces")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.SkipPermissionsRun("prompt", harness.LaunchOpts{WorkDir: want}); err != nil {
		t.Fatalf("SkipPermissionsRun: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(home, "captured-workdir"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("headless workdir = %q, want %q", got, want)
	}
}

// TestHeadlessRunFailureSurfacesHarnessStderr pins the debuggability
// contract for manifest-driven harnesses: when the declared argv fails,
// the harness's own message must reach the caller. Without it a wrong
// flag or a default cap in the harness's headless mode is invisible —
// `flow done` can only say "exit status 1".
func TestHeadlessRunFailureSurfacesHarnessStderr(t *testing.T) {
	a := loadSpec(t, minimalManifest+`

[headless]
run_argv = ["sh", "-c", "echo 'native: agent: max turns reached' >&2; exit 1"]
`)
	err := a.SkipPermissionsRun("prompt", harness.LaunchOpts{})
	if err == nil {
		t.Fatal("SkipPermissionsRun on a failing argv returned nil")
	}
	if !strings.Contains(err.Error(), "native: agent: max turns reached") {
		t.Errorf("error dropped the harness's stderr: %v", err)
	}
}

func skillTree() fstest.MapFS {
	return fstest.MapFS{
		"SKILL.md":            &fstest.MapFile{Data: []byte("---\nname: flow\ndescription: task manager\n---\n\nbody\n")},
		"references/setup.md": &fstest.MapFile{Data: []byte("setup\n")},
	}
}

// pointerManifest models the common case the design exists for: a
// harness with no skill mechanism at all, reached through its ambient
// instructions file, with hooks delivered as a self-invocation
// directive.
const pointerManifest = `
schema      = 1
name        = "codex"
binary      = "codex"
session_env = "CODEX_SESSION_ID"

[session]
strategy = "uuid7"
validate = '^[a-z0-9-]+$'

[launch]
argv = ["codex", "{{.Prompt}}"]

[liveness]
probe = "none"

[skills]
discovery = "pointer"
dir       = "{{.Home}}/.codex/skills/flow"
require_frontmatter = ["name", "description"]

[skills.pointer]
file    = "{{.Home}}/.codex/AGENTS.md"
comment = "html"
block   = """
# flow
When the user asks about tasks or projects, read {{.SkillPath}} and follow it.
{{.HookDirective}}
"""

[hooks]
strategies = ["prompt-prelude", "instruction-directive"]

[vocab]
product      = "Codex"
context_file = "AGENTS.md"
`

func loadSpec(t *testing.T, manifest string) *Adapter {
	t.Helper()
	s, err := Decode([]byte(manifest), "test.toml")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	a, err := New(s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

// TestPointerInstallWritesTreeAndBlock is the end-to-end proof that a
// harness with no skill mechanism still gets a working skill.
func TestPointerInstallWritesTreeAndBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a := loadSpec(t, pointerManifest)

	if err := a.Skills().InstallSkill(skillTree()); err != nil {
		t.Fatalf("InstallSkill: %v", err)
	}

	// The tree lands where the manifest said.
	for _, rel := range []string{"SKILL.md", "references/setup.md"} {
		if _, err := os.Stat(filepath.Join(home, ".codex", "skills", "flow", rel)); err != nil {
			t.Errorf("skill file %s missing: %v", rel, err)
		}
	}

	// And the harness is TOLD about it, which is the part a native
	// harness gets for free and this one does not.
	agents := filepath.Join(home, ".codex", "AGENTS.md")
	data, err := os.ReadFile(agents)
	if err != nil {
		t.Fatalf("AGENTS.md not written: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"<!-- flow:managed:start -->",
		filepath.Join(home, ".codex", "skills", "flow", "SKILL.md"),
		"flow hook session-start", // the instruction-directive text
		"<!-- flow:managed:end -->",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("pointer block missing %q:\n%s", want, got)
		}
	}
}

// TestHookDirectiveOnlyWhenStrategyEnabled: a manifest that references
// {{.HookDirective}} without enabling the strategy must not tell the
// agent to call hooks that were wired some other way.
func TestHookDirectiveOnlyWhenStrategyEnabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	noDirective := strings.Replace(pointerManifest,
		`strategies = ["prompt-prelude", "instruction-directive"]`,
		`strategies = ["prompt-prelude"]`, 1)
	a := loadSpec(t, noDirective)

	if err := a.Skills().InstallSkill(skillTree()); err != nil {
		t.Fatalf("InstallSkill: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".codex", "AGENTS.md"))
	if strings.Contains(string(data), "flow hook session-start") {
		t.Errorf("hook directive emitted without the strategy enabled:\n%s", data)
	}
	if !strings.Contains(string(data), "SKILL.md") {
		t.Errorf("pointer block lost its skill path:\n%s", data)
	}
}

// TestPointerUninstallLeavesUserContent: the instructions file belongs
// to the user; uninstall must take only flow's region.
func TestPointerUninstallLeavesUserContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	agents := filepath.Join(home, ".codex", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(agents), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agents, []byte("# my rules\n\nalways run tests\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := loadSpec(t, pointerManifest)
	if err := a.Skills().InstallSkill(skillTree()); err != nil {
		t.Fatal(err)
	}
	if err := a.Skills().UninstallSkill(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(agents)
	if err != nil {
		t.Fatalf("uninstall deleted a file it did not create: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "always run tests") {
		t.Errorf("uninstall ate the user's content:\n%s", got)
	}
	if strings.Contains(got, "flow:managed") {
		t.Errorf("uninstall left flow's markers:\n%s", got)
	}
}

// TestFrontmatterGateBlocksSilentRejection: praxis rejects a SKILL.md
// with no `description:`. Catching that BEFORE writing turns a silent
// "flow doesn't work" into an actionable error.
func TestFrontmatterGateBlocksSilentRejection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a := loadSpec(t, pointerManifest)

	bad := fstest.MapFS{
		"SKILL.md": &fstest.MapFile{Data: []byte("---\nname: flow\n---\n\nno description\n")},
	}
	err := a.Skills().InstallSkill(bad)
	if err == nil {
		t.Fatal("a SKILL.md missing a required frontmatter key was installed anyway")
	}
	if !strings.Contains(err.Error(), "description") {
		t.Errorf("error should name the missing key: %v", err)
	}
	// Nothing may have been written.
	if _, statErr := os.Stat(filepath.Join(home, ".codex", "skills", "flow", "SKILL.md")); statErr == nil {
		t.Error("frontmatter check ran after writing; it must gate the write")
	}
}

// TestOwnsDirFalseProtectsSharedTree is the data-loss guard: praxis
// points at claude's directory, and uninstalling praxis must not delete
// claude's skill.
func TestOwnsDirFalseProtectsSharedTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	shared := strings.Replace(pointerManifest,
		`dir       = "{{.Home}}/.codex/skills/flow"`,
		"dir       = \"{{.Home}}/.claude/skills/flow\"\nowns_dir  = false", 1)
	a := loadSpec(t, shared)

	if a.Skills().OwnsSkillDir() {
		t.Fatal("OwnsSkillDir should be false when the manifest says owns_dir = false")
	}
	if err := a.Skills().InstallSkill(skillTree()); err != nil {
		t.Fatal(err)
	}
	if err := a.Skills().UninstallSkill(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "flow", "SKILL.md")); err != nil {
		t.Errorf("uninstall deleted a skill tree the manifest does not own: %v", err)
	}
	// But its own pointer block must still be gone.
	data, _ := os.ReadFile(filepath.Join(home, ".codex", "AGENTS.md"))
	if strings.Contains(string(data), "flow:managed") {
		t.Errorf("uninstall kept the pointer block:\n%s", data)
	}
}

const configPatchManifest = `
schema      = 1
name        = "demo"
binary      = "demo"
session_env = "DEMO_SESSION_ID"

[session]
strategy = "uuid4"
validate = '^[a-z0-9-]+$'

[launch]
argv = ["demo", "{{.Prompt}}"]

[liveness]
probe = "none"

[hooks]
strategies = ["config-patch"]

[hooks.config_patch]
file = "{{.Home}}/.demo/settings.json"

[hooks.config_patch.events.SessionStart]
pointer = "/hooks/SessionStart"
entry   = '"{{.Command}}"'

[vocab]
product = "Demo"
`

// TestConfigPatchPreservesForeignKeys: flow writes into a file the
// harness owns and the user may have configured. Everything that is not
// flow's entry must survive install AND uninstall.
func TestConfigPatchPreservesForeignKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settings := filepath.Join(home, ".demo", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `{"model":"big","hooks":{"SessionStart":["someone-elses-hook"],"Other":["x"]},"theme":"dark"}`
	if err := os.WriteFile(settings, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	a := loadSpec(t, configPatchManifest)
	added, err := a.Hooks().InstallSessionStartHook("flow hook session-start")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !added {
		t.Error("install reported no change")
	}

	var got map[string]any
	data, _ := os.ReadFile(settings)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("settings is no longer valid JSON: %v", err)
	}
	if got["model"] != "big" || got["theme"] != "dark" {
		t.Errorf("unrelated top-level keys lost: %v", got)
	}
	hooks := got["hooks"].(map[string]any)
	if _, ok := hooks["Other"]; !ok {
		t.Errorf("unrelated hook event lost: %v", hooks)
	}
	ss := hooks["SessionStart"].([]any)
	if len(ss) != 2 || ss[0] != "someone-elses-hook" {
		t.Errorf("another tool's hook was displaced: %v", ss)
	}

	// Idempotent.
	added, err = a.Hooks().InstallSessionStartHook("flow hook session-start")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Error("second install reported a change")
	}

	// Uninstall removes ours and only ours.
	removed, err := a.Hooks().UninstallSessionStartHook("flow hook session-start")
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Error("uninstall reported nothing removed")
	}
	data, _ = os.ReadFile(settings)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "flow hook") {
		t.Errorf("flow's hook survived uninstall:\n%s", data)
	}
	hooks = got["hooks"].(map[string]any)
	ss = hooks["SessionStart"].([]any)
	if len(ss) != 1 || ss[0] != "someone-elses-hook" {
		t.Errorf("uninstall damaged another tool's hook: %v", ss)
	}
	if got["model"] != "big" {
		t.Errorf("uninstall lost unrelated keys: %v", got)
	}
}

// TestConfigPatchRefusesToClobberInvalidJSON: overwriting a config the
// harness needs to start would be worse than refusing.
func TestConfigPatchRefusesToClobberInvalidJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settings := filepath.Join(home, ".demo", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte("{ not json at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := loadSpec(t, configPatchManifest)
	if _, err := a.Hooks().InstallSessionStartHook("flow hook session-start"); err == nil {
		t.Fatal("wrote over a malformed config instead of refusing")
	}
	data, _ := os.ReadFile(settings)
	if string(data) != "{ not json at all" {
		t.Errorf("the malformed file was modified:\n%s", data)
	}
}

// TestConfigPatchCreatesMissingFile covers the fresh-machine case.
func TestConfigPatchCreatesMissingFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a := loadSpec(t, configPatchManifest)
	if _, err := a.Hooks().InstallSessionStartHook("flow hook session-start"); err != nil {
		t.Fatalf("install into a fresh home: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".demo", "settings.json"))
	if err != nil {
		t.Fatalf("config not created: %v", err)
	}
	if !strings.Contains(string(data), "flow hook session-start") {
		t.Errorf("hook not written:\n%s", data)
	}
}

// TestUnknownEventIsNotAnError: a harness may support SessionStart and
// have no per-prompt event. Asking for the one it lacks must be a
// quiet no-op, not a failure that aborts install.
func TestUnknownEventIsNotAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a := loadSpec(t, configPatchManifest) // declares SessionStart only
	added, err := a.Hooks().InstallUserPromptSubmitHook("flow hook user-prompt-submit")
	if err != nil {
		t.Fatalf("installing an unsupported event errored: %v", err)
	}
	if added {
		t.Error("reported installing an event the harness does not declare")
	}
}

// TestHookCapabilityAbsentWithoutTable pins the nil-accessor contract.
func TestHookCapabilityAbsentWithoutTable(t *testing.T) {
	a := loadSpec(t, minimalManifest)
	if a.Hooks() != nil {
		t.Error("Hooks() should be nil when [hooks] is absent")
	}
	if a.Skills() != nil {
		t.Error("Skills() should be nil when [skills] is absent")
	}
}

// TestInstructionDirectiveRequiresPointer: the directive is delivered
// inside the pointer block, so enabling it without one would silently
// deliver nothing.
func TestInstructionDirectiveRequiresPointer(t *testing.T) {
	broken := strings.Replace(pointerManifest, `discovery = "pointer"`, `discovery = "native"`, 1)
	_, err := Decode([]byte(broken), "t.toml")
	if err == nil {
		t.Fatal("instruction-directive without a pointer block was accepted")
	}
	if !strings.Contains(err.Error(), "instruction-directive") {
		t.Errorf("error should explain the coupling: %v", err)
	}
}
