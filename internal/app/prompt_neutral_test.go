package app

import (
	"path/filepath"
	"strings"
	"testing"
)

// Every session-starting prompt flow writes is read by Claude Code OR
// Codex. Each must say how to load the flow skill without a Skill tool
// (the real Codex SKILL.md path) and name AGENTS.md beside CLAUDE.md.
func TestPromptsAreHarnessNeutral(t *testing.T) {
	setupFlowRoot(t)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	codexSkill := filepath.Join(codexHome, "skills", "flow", "SKILL.md")
	prompts := map[string]string{
		"task":           buildTaskBootstrapPrompt("t1"),
		"playbook-run":   buildPlaybookRunBootstrapPrompt("r1", "pb", false),
		"playbook-first": buildPlaybookRunBootstrapPrompt("r1", "pb", true),
		"auto":           buildAutoBootstrapPrompt("t1", "regular", "", ""),
		"auto-child":     buildAutoBootstrapPrompt("t1", "regular", "", "parent"),
		"sweep":          buildCloseoutSweepPrompt("t1", "proj"),
		"owner":          buildOwnerTickPrompt("o1", "/x/owners/o1"),
		"owner-inter":    buildOwnerTickPromptInteractive("o1", "/x/owners/o1"),
	}
	for name, p := range prompts {
		if !strings.Contains(p, codexSkill) {
			t.Errorf("%s prompt lacks the Codex skill path %s:\n%s", name, codexSkill, p)
		}
		if strings.Contains(p, "Invoke the flow skill via the Skill tool.") ||
			strings.Contains(p, "Invoke the flow skill via the Skill tool —") {
			t.Errorf("%s prompt still assumes a Skill tool:\n%s", name, p)
		}
		if strings.Contains(p, "CLAUDE.md") && !strings.Contains(p, "AGENTS.md") {
			t.Errorf("%s prompt names CLAUDE.md without AGENTS.md:\n%s", name, p)
		}
		if strings.Contains(p, "Claude session") {
			t.Errorf("%s prompt says Claude session:\n%s", name, p)
		}
	}
}
