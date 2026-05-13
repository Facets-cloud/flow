package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse settings: %v\nraw: %s", err, raw)
	}
	return m
}

func hookEventReferencesCommand(hooks map[string]any, event, command string) bool {
	entries, _ := hooks[event].([]any)
	return countMatchingHookEntries(entries, command) >= 1
}

func countMatchingHookEntries(entries []any, command string) int {
	n := 0
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		inner, _ := m["hooks"].([]any)
		for _, h := range inner {
			hm, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmd, _ := hm["command"].(string); cmd == command {
				n++
				break
			}
		}
	}
	return n
}

func expectedCommand(event string) string {
	switch event {
	case "SessionStart":
		return "flow hook session-start"
	case "UserPromptSubmit":
		return "flow hook user-prompt-submit"
	}
	return ""
}

// withTempHome redirects $HOME to a tempdir for the duration of the test.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	t.Cleanup(func() { os.Setenv("HOME", oldHome) })
	return dir
}

func TestSkillInstallWritesFile(t *testing.T) {
	home := withTempHome(t)

	rc := cmdSkill([]string{"install"})
	if rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	path := filepath.Join(home, ".claude", "skills", "flow", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "name: flow") {
		t.Errorf("installed skill missing frontmatter 'name: flow'")
	}
	if !strings.Contains(string(data), "---") {
		t.Errorf("installed skill missing YAML frontmatter delimiters")
	}
}

func TestSkillFrontmatterDescriptionFitsCodexLimit(t *testing.T) {
	got := string(embeddedSkill)
	const marker = "description: |"
	start := strings.Index(got, marker)
	if start == -1 {
		t.Fatal("skill frontmatter missing description block")
	}
	rest := got[start+len(marker):]
	end := strings.Index(rest, "\n---")
	if end == -1 {
		t.Fatal("skill frontmatter missing closing delimiter")
	}

	var desc strings.Builder
	for _, line := range strings.Split(rest[:end], "\n") {
		if line == "" {
			continue
		}
		desc.WriteString(strings.TrimPrefix(line, "  "))
		desc.WriteByte('\n')
	}
	if n := len(strings.TrimSpace(desc.String())); n > 1024 {
		t.Fatalf("skill description is %d bytes, exceeds Codex limit of 1024", n)
	}
}

func TestSkillInstallErrorsOnExisting(t *testing.T) {
	withTempHome(t)
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("first install rc=%d", rc)
	}
	if rc := cmdSkill([]string{"install"}); rc == 0 {
		t.Errorf("second install without --force should fail, got rc=%d", rc)
	}
}

func TestSkillInstallForceOverwrites(t *testing.T) {
	home := withTempHome(t)
	path := filepath.Join(home, ".claude", "skills", "flow", "SKILL.md")

	// Pre-create something different.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("something else"), 0o644); err != nil {
		t.Fatal(err)
	}

	if rc := cmdSkill([]string{"install", "--force"}); rc != 0 {
		t.Fatalf("install --force rc=%d", rc)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "something else" {
		t.Error("install --force did not overwrite existing file")
	}
}

func TestSkillUpdateIsForceInstall(t *testing.T) {
	withTempHome(t)
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("first install rc=%d", rc)
	}
	// `update` should succeed even though file exists.
	if rc := cmdSkill([]string{"update"}); rc != 0 {
		t.Errorf("update rc=%d, want 0", rc)
	}
}

func TestSkillUninstallRemovesDir(t *testing.T) {
	home := withTempHome(t)
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	dir := filepath.Join(home, ".claude", "skills", "flow")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("skill dir missing after install: %v", err)
	}
	if rc := cmdSkill([]string{"uninstall"}); rc != 0 {
		t.Fatalf("uninstall rc=%d", rc)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("skill dir still present after uninstall: %v", err)
	}
}

func TestSkillUninstallIdempotent(t *testing.T) {
	withTempHome(t)
	// Nothing installed — uninstall should still succeed.
	if rc := cmdSkill([]string{"uninstall"}); rc != 0 {
		t.Errorf("uninstall on empty home rc=%d", rc)
	}
}

// TestSkillInstallWritesSessionStartHook verifies install wires up the
// SessionStart hook into ~/.claude/settings.json. The UserPromptSubmit
// hook was retired in v0.1.0-alpha.7 — install MUST NOT add it.
func TestSkillInstallWritesSessionStartHook(t *testing.T) {
	home := withTempHome(t)
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	settings := readSettings(t, filepath.Join(home, ".claude", "settings.json"))
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatal("settings.json has no hooks key")
	}
	if !hookEventReferencesCommand(hooks, "SessionStart", "flow hook session-start") {
		t.Errorf("SessionStart hook missing or wrong command: %#v", hooks["SessionStart"])
	}
	// UserPromptSubmit must not be installed by fresh install.
	if hookEventReferencesCommand(hooks, "UserPromptSubmit", "flow hook user-prompt-submit") {
		t.Errorf("install should NOT add UserPromptSubmit hook; got %#v", hooks["UserPromptSubmit"])
	}
}

// TestSkillInstallIsIdempotent verifies a second install --force does
// not duplicate the SessionStart entry. Past regressions append
// duplicates silently; pin against that.
func TestSkillInstallIsIdempotent(t *testing.T) {
	home := withTempHome(t)
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("first install rc=%d", rc)
	}
	if rc := cmdSkill([]string{"install", "--force"}); rc != 0 {
		t.Fatalf("second install --force rc=%d", rc)
	}
	settings := readSettings(t, filepath.Join(home, ".claude", "settings.json"))
	hooks, _ := settings["hooks"].(map[string]any)
	entries, _ := hooks["SessionStart"].([]any)
	if got := countMatchingHookEntries(entries, expectedCommand("SessionStart")); got != 1 {
		t.Errorf("SessionStart: got %d matching entries, want 1", got)
	}
}

// TestSkillInstallRemovesStaleUserPromptSubmit verifies the upgrade
// path: an existing settings.json with a UserPromptSubmit hook entry
// (legacy install from <= v0.1.0-alpha.6) gets that entry removed by
// `flow skill install --force`, even on a fresh skill install.
func TestSkillInstallRemovesStaleUserPromptSubmit(t *testing.T) {
	home := withTempHome(t)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := `{
  "hooks": {
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": "flow hook user-prompt-submit"}]}
    ]
  }
}`
	if err := os.WriteFile(settingsPath, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	settings := readSettings(t, settingsPath)
	hooks, _ := settings["hooks"].(map[string]any)
	if hookEventReferencesCommand(hooks, "UserPromptSubmit", "flow hook user-prompt-submit") {
		t.Errorf("install should remove stale UserPromptSubmit hook; got %#v", hooks["UserPromptSubmit"])
	}
	// SessionStart should still get installed normally.
	if !hookEventReferencesCommand(hooks, "SessionStart", "flow hook session-start") {
		t.Errorf("SessionStart hook missing after install: %#v", hooks["SessionStart"])
	}
}

// TestSkillInstallPreservesUnrelatedHooks pins the safety property:
// removing flow's UserPromptSubmit hook MUST NOT touch other commands
// the user has wired under UserPromptSubmit (e.g. their own scripts),
// and MUST NOT touch other event keys (SessionEnd, PreToolUse, etc.).
func TestSkillInstallPreservesUnrelatedHooks(t *testing.T) {
	home := withTempHome(t)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	mixed := `{
  "hooks": {
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": "flow hook user-prompt-submit"}]},
      {"hooks": [{"type": "command", "command": "my-custom-tool --watch"}]}
    ],
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "user-pretool-script"}]}
    ]
  },
  "someUnrelatedKey": "preserve-me"
}`
	if err := os.WriteFile(settingsPath, []byte(mixed), 0o644); err != nil {
		t.Fatal(err)
	}

	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	settings := readSettings(t, settingsPath)

	// flow's UPS entry gone; user's UPS entry preserved.
	hooks, _ := settings["hooks"].(map[string]any)
	if hookEventReferencesCommand(hooks, "UserPromptSubmit", "flow hook user-prompt-submit") {
		t.Errorf("flow UPS hook should be removed; got %#v", hooks["UserPromptSubmit"])
	}
	if !hookEventReferencesCommand(hooks, "UserPromptSubmit", "my-custom-tool --watch") {
		t.Errorf("user's UPS hook MUST be preserved; got %#v", hooks["UserPromptSubmit"])
	}

	// PreToolUse untouched.
	if !hookEventReferencesCommand(hooks, "PreToolUse", "user-pretool-script") {
		t.Errorf("PreToolUse hook MUST be preserved; got %#v", hooks["PreToolUse"])
	}

	// Top-level non-hook key untouched.
	if v, _ := settings["someUnrelatedKey"].(string); v != "preserve-me" {
		t.Errorf("unrelated top-level key should be preserved, got %v", settings["someUnrelatedKey"])
	}
}

// TestSkillUninstallRemovesSessionStartHook verifies uninstall strips
// the SessionStart entry. (The UserPromptSubmit hook is no longer
// installed, so its absence after uninstall is verified by the
// install-side tests.)
func TestSkillUninstallRemovesSessionStartHook(t *testing.T) {
	home := withTempHome(t)
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	if rc := cmdSkill([]string{"uninstall"}); rc != 0 {
		t.Fatalf("uninstall rc=%d", rc)
	}
	settings := readSettings(t, filepath.Join(home, ".claude", "settings.json"))
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks != nil && len(hooks) != 0 {
		t.Errorf("expected hooks map empty or absent after uninstall, got %#v", hooks)
	}
}

// TestSkillInstallSkipHook leaves settings.json untouched when --skip-hook.
func TestSkillInstallSkipHook(t *testing.T) {
	home := withTempHome(t)
	if rc := cmdSkill([]string{"install", "--skip-hook"}); rc != 0 {
		t.Fatalf("install --skip-hook rc=%d", rc)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Errorf("--skip-hook should not create settings.json; stat err=%v", err)
	}
}

// TestSkillTargetsClaudeAlwaysPresent verifies Claude is in the
// returned target list even when ~/.claude doesn't exist on disk —
// preserves the legacy behavior where `flow init` always provisions
// the Claude skill dir.
func TestSkillTargetsClaudeAlwaysPresent(t *testing.T) {
	withTempHome(t)
	targets, err := skillTargets()
	if err != nil {
		t.Fatalf("skillTargets: %v", err)
	}
	if len(targets) != 1 || targets[0].agent != "claude" {
		t.Errorf("with empty home, want only claude target; got %+v", targets)
	}
}

// TestSkillTargetsDiscoversInstalledAgents seeds ~/.codex, ~/.cursor,
// ~/.gemini in the tempdir and verifies they all show up alongside
// Claude.
func TestSkillTargetsDiscoversInstalledAgents(t *testing.T) {
	home := withTempHome(t)
	for _, sub := range []string{".codex", ".cursor", ".gemini"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	targets, err := skillTargets()
	if err != nil {
		t.Fatalf("skillTargets: %v", err)
	}
	gotAgents := make(map[string]string)
	for _, t := range targets {
		gotAgents[t.agent] = t.skillPath
	}
	for _, want := range []string{"claude", "codex", "cursor", "gemini"} {
		if _, ok := gotAgents[want]; !ok {
			t.Errorf("missing %s target; got %+v", want, gotAgents)
		}
	}
	// Spot-check one path shape so future renames stay honest.
	want := filepath.Join(home, ".codex", "skills", "flow", "SKILL.md")
	if got := gotAgents["codex"]; got != want {
		t.Errorf("codex skillPath=%q, want %q", got, want)
	}
}

// TestSkillInstallWritesAllAgentTargets verifies `flow skill install`
// drops SKILL.md into every discovered agent's install dir. Gemini's
// install dir is `~/.agents/skills/flow/` (NOT `~/.gemini/`) because
// Gemini scans the shared `~/.agents/skills/` root.
func TestSkillInstallWritesAllAgentTargets(t *testing.T) {
	home := withTempHome(t)
	for _, sub := range []string{".codex", ".cursor", ".gemini"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}

	// agent -> install subdir under $HOME.
	wantPaths := map[string]string{
		"claude": ".claude",
		"codex":  ".codex",
		"cursor": ".cursor",
		"gemini": ".agents", // shared multi-agent skills root
	}
	for agent, sub := range wantPaths {
		p := filepath.Join(home, sub, "skills", "flow", "SKILL.md")
		data, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("missing SKILL.md for %s at %s: %v", agent, p, err)
			continue
		}
		if !strings.Contains(string(data), "name: flow") {
			t.Errorf("SKILL.md at %s missing frontmatter", p)
		}
		// VERSION sidecar should be written per agent.
		if _, err := os.Stat(filepath.Join(filepath.Dir(p), "VERSION")); err != nil {
			t.Errorf("missing VERSION sidecar for %s: %v", agent, err)
		}
	}

	// Gemini-specific safety property: no SKILL.md inside ~/.gemini/skills/
	// even though `~/.gemini/` was created. Double-install would
	// duplicate-load via Gemini's two scan roots.
	if _, err := os.Stat(filepath.Join(home, ".gemini", "skills", "flow", "SKILL.md")); err == nil {
		t.Error("Gemini install must go to ~/.agents/, NOT ~/.gemini/skills/ — both scan roots would double-load")
	}
}

// TestSkillInstallSkipsAbsentAgents verifies an agent presence dir
// that doesn't exist gets no install — we MUST NOT eagerly create
// `~/.<agent>/` (or `~/.agents/`) for agents the user doesn't have.
func TestSkillInstallSkipsAbsentAgents(t *testing.T) {
	home := withTempHome(t)
	// Only ~/.codex is present; Cursor/Gemini stay absent.
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}

	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}

	// Claude + Codex SHOULD exist.
	for _, want := range []string{".claude", ".codex"} {
		if _, err := os.Stat(filepath.Join(home, want, "skills", "flow", "SKILL.md")); err != nil {
			t.Errorf("expected install for %s; got err=%v", want, err)
		}
	}
	// Cursor presence dir + Gemini presence dir + Gemini install dir
	// (~/.agents) MUST NOT have been auto-created.
	for _, absent := range []string{".cursor", ".gemini", ".agents"} {
		if _, err := os.Stat(filepath.Join(home, absent)); !os.IsNotExist(err) {
			t.Errorf("install must not create ~/%s; stat err=%v", absent, err)
		}
	}
}

// TestGeminiInstallsToAgentsRoot verifies the Gemini→.agents redirect
// in isolation: when only ~/.gemini/ exists (no other agents present),
// the install must land at ~/.agents/skills/flow/SKILL.md, NOT under
// ~/.gemini/.
func TestGeminiInstallsToAgentsRoot(t *testing.T) {
	home := withTempHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o755); err != nil {
		t.Fatal(err)
	}

	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}

	// Must exist under ~/.agents/skills/flow/.
	want := filepath.Join(home, ".agents", "skills", "flow", "SKILL.md")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("Gemini install should land at %s; stat err=%v", want, err)
	}
	// Must NOT exist under ~/.gemini/skills/flow/.
	bad := filepath.Join(home, ".gemini", "skills", "flow", "SKILL.md")
	if _, err := os.Stat(bad); err == nil {
		t.Errorf("Gemini install must NOT touch %s — would double-load", bad)
	}
}

// TestSkillInstallRefusesIfAnyTargetExists pins the safety property:
// even one existing SKILL.md across any target causes plain `install`
// (without --force) to abort the whole batch.
func TestSkillInstallRefusesIfAnyTargetExists(t *testing.T) {
	home := withTempHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-create Codex SKILL.md only.
	codexSkill := filepath.Join(home, ".codex", "skills", "flow", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(codexSkill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexSkill, []byte("preexisting"), 0o644); err != nil {
		t.Fatal(err)
	}

	if rc := cmdSkill([]string{"install"}); rc == 0 {
		t.Errorf("install must fail when any target SKILL.md exists; got rc=0")
	}
	// Pre-existing file MUST NOT be modified.
	got, _ := os.ReadFile(codexSkill)
	if string(got) != "preexisting" {
		t.Errorf("refused install must not overwrite existing %s", codexSkill)
	}
	// And Claude MUST NOT have been written either (batch-or-nothing).
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "flow", "SKILL.md")); err == nil {
		t.Errorf("refused install must not write Claude either; found one")
	}
}

// TestSkillUpdateOverwritesAllAgents — `flow skill update` is force=true.
// Should refresh SKILL.md in every discovered agent's install dir
// (Gemini's is `.agents`, not `.gemini`).
func TestSkillUpdateOverwritesAllAgents(t *testing.T) {
	home := withTempHome(t)
	for _, sub := range []string{".codex", ".gemini"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	installSubdirs := []string{".claude", ".codex", ".agents"}
	// Stomp every installed SKILL.md so we can prove update refreshes them.
	for _, sub := range installSubdirs {
		p := filepath.Join(home, sub, "skills", "flow", "SKILL.md")
		if err := os.WriteFile(p, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if rc := cmdSkill([]string{"update"}); rc != 0 {
		t.Fatalf("update rc=%d", rc)
	}

	for _, sub := range installSubdirs {
		got, _ := os.ReadFile(filepath.Join(home, sub, "skills", "flow", "SKILL.md"))
		if string(got) == "stale" {
			t.Errorf("update did not refresh %s SKILL.md", sub)
		}
	}
}

// TestSkillUninstallRemovesAllAgents verifies uninstall sweeps the
// flow/ dir from every agent's skills/ tree.
func TestSkillUninstallRemovesAllAgents(t *testing.T) {
	home := withTempHome(t)
	for _, sub := range []string{".codex", ".cursor"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	if rc := cmdSkill([]string{"uninstall"}); rc != 0 {
		t.Fatalf("uninstall rc=%d", rc)
	}
	for _, agent := range []string{".claude", ".codex", ".cursor"} {
		p := filepath.Join(home, agent, "skills", "flow")
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("uninstall should remove %s; stat err=%v", p, err)
		}
	}
}

// TestMaybeAutoUpgradeRefreshesAllAgents — version mismatch should
// refresh SKILL.md in every installed agent target, not just Claude.
// Includes the Gemini→.agents redirect.
func TestMaybeAutoUpgradeRefreshesAllAgents(t *testing.T) {
	home := withTempHome(t)
	for _, sub := range []string{".codex", ".cursor", ".gemini"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	withVersion(t, "v1.0.0")
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	// Stomp every SKILL.md so we can detect the refresh. Gemini's
	// install dir is `.agents`, not `.gemini`.
	paths := []string{}
	for _, sub := range []string{".claude", ".codex", ".cursor", ".agents"} {
		p := filepath.Join(home, sub, "skills", "flow", "SKILL.md")
		paths = append(paths, p)
		if err := os.WriteFile(p, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	Version = "v2.0.0"
	maybeAutoUpgradeSkill()

	for _, p := range paths {
		got, _ := os.ReadFile(p)
		if string(got) == "stale" {
			t.Errorf("auto-upgrade did not refresh %s", p)
		}
		v := readVersionAt(filepath.Join(filepath.Dir(p), "VERSION"))
		if v != "v2.0.0" {
			t.Errorf("%s VERSION=%q, want v2.0.0", p, v)
		}
	}
}

func TestSkillUnknownSubcommand(t *testing.T) {
	if rc := cmdSkill([]string{"wat"}); rc != 2 {
		t.Errorf("unknown subcommand rc=%d, want 2", rc)
	}
	if rc := cmdSkill(nil); rc != 2 {
		t.Errorf("missing subcommand rc=%d, want 2", rc)
	}
}

func TestSkillMentionsPlaybooks(t *testing.T) {
	got := string(embeddedSkill)
	for _, want := range []string{
		"## 2. The model",
		"**Playbooks**",
		"flow add playbook",
		"flow run playbook",
		"flow list playbooks",
		"flow show playbook",
		"flow list runs",
		"Active playbooks",
		"playbooks/<slug>/updates/",
		"playbook definitions are never \"done\" — they're archived",
		"flow archive <playbook-slug>",
		"## Playbook activity",
		"Each run does",
		"Signals to watch for",
		"Do not auto-fire `flow run playbook`",
		"snapshot",
		"Do not propose scheduling during playbook intake",
		"the bootstrapped task\" includes playbook-run tasks",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skill missing %q", want)
		}
	}
}

func TestSkillHasPlaybookSections(t *testing.T) {
	got := string(embeddedSkill)
	for _, want := range []string{
		"### 4.12 Add a playbook",
		"### 4.13 Run a playbook",
		"fire the X agent",
		"kind: playbook_run",
		"snapshot taken when this run started",
		"Files listed under `other:`",
		"load on demand",
		"Auxiliary files in entity directories",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skill missing %q", want)
		}
	}
}

func TestSkillSection414(t *testing.T) {
	got := string(embeddedSkill)
	for _, want := range []string{
		"### 4.14 Substantive-unrelated-work check",
		"ongoing check, not one-shot",
		"superpowers:brainstorming",
		"Re-evaluate on every turn",
		"Process-skill ordering",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skill missing %q", want)
		}
	}
}

func TestSkillIntakeMinimal(t *testing.T) {
	got := string(embeddedSkill)
	for _, want := range []string{
		"Required sections (always asked, in this order)",
		"Optional sections (offered, can be deferred)",
		"Detail now",
		"Defer until you start the task",
		"Thin task brief (intake-minimal)",
		"*Deferred — fill in at task start.*",
		"Deferred-section prompt",
		"Fill in now",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skill missing %q", want)
		}
	}
}

func TestSkillUsesAskUserQuestionConsistently(t *testing.T) {
	got := string(embeddedSkill)
	// The skill should have many AskUserQuestion references — at least one
	// per major workflow that involves user choice.
	count := strings.Count(got, "AskUserQuestion")
	if count < 40 {
		t.Errorf("expected at least 40 AskUserQuestion references in skill, got %d", count)
	}
	// §4a should set the policy explicitly.
	if !strings.Contains(got, "always AskUserQuestion") {
		t.Errorf("skill §4a should establish 'always AskUserQuestion' as the rule")
	}
}

func TestSkillHasPlaybookPersistAdjustmentsPattern(t *testing.T) {
	got := string(embeddedSkill)
	for _, want := range []string{
		"Persisting in-run adjustments back to the playbook",
		"frozen snapshot",
		"playbooks/<slug>/brief.md",
		"Persist to playbook",
		"Just this run",
		"Never edit the run-task's own `brief.md`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skill missing %q", want)
		}
	}
}

func TestSkillHasMidInterviewDriftRule(t *testing.T) {
	got := string(embeddedSkill)
	for _, want := range []string{
		"Mid-interview drift",
		"sub-question has 2–4 discrete options",
		"Don't keep typing prose just because you started",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skill missing mid-interview-drift content %q", want)
		}
	}
}

// TestSkillHasUpgradeWorkflow pins the §4.15 upgrade procedure: the
// skill must know how to walk the user through replacing the binary
// per the README at https://github.com/Facets-cloud/flow and then
// running `flow skill update`. It must also recognize the
// `flow-version-stale:` signal the SessionStart hook emits when the
// local binary lags the latest GitHub release.
func TestSkillHasUpgradeWorkflow(t *testing.T) {
	got := string(embeddedSkill)
	for _, want := range []string{
		"### 4.15 Upgrade flow itself",
		"https://github.com/Facets-cloud/flow",
		"flow --version",
		"flow skill update",
		"flow-version-stale:",
		"xattr -d com.apple.quarantine",
		"Do not invent download URLs",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skill missing upgrade workflow content: %q", want)
		}
	}
}

// TestSkillEmphasizesCloseOutValue pins §4.7's framing of `flow done`
// as the load-bearing moment that persists the session's learnings —
// it triggers the close-out sweep that writes KB + project update.
// Without this content the skill treats closure as bookkeeping and
// Claude never proactively offers to close, which means the user's
// learnings stay locked in the transcript.
func TestSkillEmphasizesCloseOutValue(t *testing.T) {
	got := string(embeddedSkill)
	for _, want := range []string{
		"Why closing matters",
		"close-out sweep",
		"that distillation never happens",
		"silent loss of durable knowledge",
		"Recognizing natural close-out moments",
		// Expanded trigger list must include real-world wrap-up phrasing,
		// not just the literal verbs the old skill listed.
		"shipped",
		"PR merged",
		"deployed",
		"that's working",
		// Matching §8 anti-pattern reinforces the rule.
		"Do not let work wrap up without prompting closure",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skill missing close-out emphasis: %q", want)
		}
	}
}

// TestSkillHasAccessibilityErrorRecipe pins the §4.4 recipe for
// handling the macOS Accessibility error from the Terminal.app
// backend: name Terminal definitively (not Claude/flow), open the
// right Settings pane via the deep-link URL, and retry only after
// explicit user confirmation.
func TestSkillHasAccessibilityErrorRecipe(t *testing.T) {
	got := string(embeddedSkill)
	for _, want := range []string{
		"macOS Accessibility error from the Terminal.app backend",
		"Trust the error verbatim",
		"NOT Claude Code",
		"NOT the flow binary",
		"x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
		"there is no CLI to",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skill missing Accessibility recipe: %q", want)
		}
	}
}

// TestSkillHasExplicitInvocationSection pins the §1a behavior: when
// the skill is invoked without a trigger phrase, it should describe
// its capabilities and AskUserQuestion for the user's intent — NOT
// auto-run §4.1, auto-list tasks, or auto-propose opening a task.
func TestSkillHasExplicitInvocationSection(t *testing.T) {
	got := string(embeddedSkill)
	for _, want := range []string{
		"## 1a. When invoked explicitly with no intent",
		"DO NOT auto-run any workflow",
		"do not enter §4.1",
		`What now?`,
		"Just exploring",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skill missing §1a content: %q", want)
		}
	}
}

// TestSkillNoCliCoachingInUserFacingLabels pins the rule that the
// skill must not put literal `flow ...` invocations inside
// AskUserQuestion option labels or chat replies. Users should never
// see CLI commands; Claude uses flow under the hood.
//
// We pin two specific past offenders that motivated the sweep — the
// "Run init?" prompt that read "Yes, run flow init", and the
// "Mark done?" prompt that read "Yes, `flow done <slug>`". If either
// regresses, a future sweep loses ground silently.
func TestSkillNoCliCoachingInUserFacingLabels(t *testing.T) {
	got := string(embeddedSkill)
	for _, banned := range []string{
		`"Yes, run flow init"`,
		"\"Yes, `flow done <slug>`\"",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("user-facing label still exposes CLI: %s", banned)
		}
	}
	// And the rule itself must be present in §8.
	for _, want := range []string{
		"Do not surface flow commands to the user",
		"users never need to learn the CLI",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skill missing CLI-coaching anti-pattern: %q", want)
		}
	}
}

func TestSkillHasFirstRunCapturePattern(t *testing.T) {
	got := string(embeddedSkill)
	for _, want := range []string{
		"First-run capture",
		"FIRST RUN OF THIS PLAYBOOK",
		"crystallizes",
		"Save as sidecar file",
		"Capture anything from this run back to the playbook",
		"Capture-back is a primary deliverable of the first run",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skill missing first-run capture content %q", want)
		}
	}
}

func TestPlaybookRunBootstrapMentionsPersistAdjustments(t *testing.T) {
	prompt := buildPlaybookRunBootstrapPrompt("p--2026-04-30-10-30", "p", false)
	for _, want := range []string{
		"adjusts the playbook",
		"AskUserQuestion",
		"Persist to playbook",
		"playbooks/p/brief.md",
		"frozen snapshot",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("playbook-run bootstrap prompt missing %q; got:\n%s", want, prompt)
		}
	}
}
