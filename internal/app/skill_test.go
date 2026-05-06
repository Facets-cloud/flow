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

// TestSkillInstallWritesBothHooks verifies install wires up BOTH the
// SessionStart hook (existing behavior) and the new UserPromptSubmit
// hook into ~/.claude/settings.json.
func TestSkillInstallWritesBothHooks(t *testing.T) {
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
	if !hookEventReferencesCommand(hooks, "UserPromptSubmit", "flow hook user-prompt-submit") {
		t.Errorf("UserPromptSubmit hook missing or wrong command: %#v", hooks["UserPromptSubmit"])
	}
}

// TestSkillInstallIsIdempotent verifies a second install --force does
// not duplicate either hook entry. Past regressions append duplicates
// silently; pin against that.
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
	for _, event := range []string{"SessionStart", "UserPromptSubmit"} {
		entries, _ := hooks[event].([]any)
		if got := countMatchingHookEntries(entries, expectedCommand(event)); got != 1 {
			t.Errorf("%s: got %d matching entries, want 1", event, got)
		}
	}
}

// TestSkillUninstallRemovesBothHooks verifies uninstall strips both
// hook entries and ends with an empty hooks map (or no hooks key).
func TestSkillUninstallRemovesBothHooks(t *testing.T) {
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
