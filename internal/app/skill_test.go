package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
