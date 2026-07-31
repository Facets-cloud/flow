package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCmdInitInstallsPraxisIntegrationWithoutSelection proves an ordinary
// `flow init` wires every registered harness. A normal shell has neither
// CLAUDE_CODE_SESSION_ID nor PRAXIS_SESSION_ID, so selecting one from ambient
// state would be guesswork; installing both integrations is deterministic.
func TestCmdInitInstallsPraxisIntegrationWithoutSelection(t *testing.T) {
	initTempFlowRoot(t)
	if rc := cmdInit(nil); rc != 0 {
		t.Fatalf("cmdInit rc=%d", rc)
	}

	home := os.Getenv("HOME")
	skillPath := filepath.Join(home, ".praxis", "agent", "skills", "flow", "SKILL.md")
	if info, err := os.Stat(skillPath); err != nil {
		t.Fatalf("Praxis SKILL.md missing: %v", err)
	} else if info.Size() == 0 {
		t.Error("Praxis SKILL.md is empty")
	}

	settingsPath := filepath.Join(home, ".praxis", "agent", "settings.json")
	settings, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("Praxis settings missing: %v", err)
	}
	for _, want := range []string{
		"SessionStart", "flow hook session-start", "startup|resume",
		// The UserPromptSubmit hook carries the drift/close-out anchor;
		// init and `flow skill install` must wire the same set.
		"UserPromptSubmit", "flow hook user-prompt-submit",
	} {
		if !strings.Contains(string(settings), want) {
			t.Errorf("Praxis settings missing %q:\n%s", want, settings)
		}
	}

	claudeSkill := filepath.Join(home, ".claude", "skills", "flow", "SKILL.md")
	if _, err := os.Stat(claudeSkill); err != nil {
		t.Errorf("Claude SKILL.md missing: %v", err)
	}
}

// Install and uninstall must cover the same set of harnesses. When they
// diverge, `flow skill uninstall` leaves another agent's skill directory
// and — worse — a live `flow hook` entry in its settings.json, so that
// agent keeps shelling out to a flow the user thinks they removed.
func TestSkillUninstallIsTheInverseOfInit(t *testing.T) {
	initTempFlowRoot(t)
	if rc := cmdInit(nil); rc != 0 {
		t.Fatalf("cmdInit rc=%d", rc)
	}
	home := os.Getenv("HOME")

	skillDirs := map[string]string{
		"claude": filepath.Join(home, ".claude", "skills", "flow"),
		"praxis": filepath.Join(home, ".praxis", "agent", "skills", "flow"),
	}
	settingsPaths := map[string]string{
		"claude": filepath.Join(home, ".claude", "settings.json"),
		"praxis": filepath.Join(home, ".praxis", "agent", "settings.json"),
	}
	for name, dir := range skillDirs {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("%s skill dir missing after init: %v", name, err)
		}
	}

	if rc := cmdSkill([]string{"uninstall"}); rc != 0 {
		t.Fatalf("cmdSkill uninstall rc=%d", rc)
	}

	for name, dir := range skillDirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s skill dir survived uninstall (stat err=%v)", name, err)
		}
	}
	for name, path := range settingsPaths {
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s settings: %v", name, err)
		}
		if strings.Contains(string(raw), "flow hook") {
			t.Errorf("%s settings still reference a flow hook after uninstall:\n%s", name, raw)
		}
	}
}

func TestCmdInitRejectsHarnessFlag(t *testing.T) {
	root := initTempFlowRoot(t)
	if rc := cmdInit([]string{"--harness", "praxis"}); rc != 2 {
		t.Fatalf("cmdInit rc=%d, want 2", rc)
	}
	if _, err := os.Stat(filepath.Join(root, "flow.db")); !os.IsNotExist(err) {
		t.Errorf("init with unsupported flag must not create a DB; stat err=%v", err)
	}
}
