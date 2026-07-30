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
	for _, want := range []string{"SessionStart", "flow hook session-start", "startup|resume"} {
		if !strings.Contains(string(settings), want) {
			t.Errorf("Praxis settings missing %q:\n%s", want, settings)
		}
	}

	claudeSkill := filepath.Join(home, ".claude", "skills", "flow", "SKILL.md")
	if _, err := os.Stat(claudeSkill); err != nil {
		t.Errorf("Claude SKILL.md missing: %v", err)
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
