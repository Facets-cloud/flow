package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/harness/claude"
)

// installSkillAs installs the skill + hooks into a temp HOME as if a
// binary of version `installed` had done it, then stomps SKILL.md and
// the Claude plugin's hooks/hooks.json with sentinels so a later refresh
// is detectable. Returns (skillPath, hooksPath).
func installSkillAs(t *testing.T, installed string) (string, string) {
	t.Helper()
	home := withTempHome(t)
	withVersion(t, installed)
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	skillPath := filepath.Join(home, ".claude", "skills", "flow", "SKILL.md")
	settingsPath := filepath.Join(home, ".claude", "skills", "flow", "hooks", "hooks.json")
	if err := os.WriteFile(skillPath, []byte("sentinel-skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"sentinel":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return skillPath, settingsPath
}

func assertUntouched(t *testing.T, skillPath, settingsPath, wantVersion string) {
	t.Helper()
	if got, _ := os.ReadFile(skillPath); string(got) != "sentinel-skill" {
		t.Errorf("SKILL.md was rewritten; want it untouched")
	}
	if got, _ := os.ReadFile(settingsPath); string(got) != `{"sentinel":true}` {
		t.Errorf("plugin hooks.json was rewritten; got %s", got)
	}
	if v := readSkillVersion(claude.New()); v != wantVersion {
		t.Errorf("VERSION sidecar=%q, want %q (untouched)", v, wantVersion)
	}
}

func assertRefreshed(t *testing.T, skillPath, settingsPath, wantVersion string) {
	t.Helper()
	if got, _ := os.ReadFile(skillPath); string(got) == "sentinel-skill" {
		t.Errorf("SKILL.md was not refreshed")
	}
	if got, _ := os.ReadFile(settingsPath); !strings.Contains(string(got), "SessionStart") {
		t.Errorf("hooks were not re-installed; hooks.json=%s", got)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Errorf("a refresh must not write ~/.claude/settings.json; stat err=%v", err)
	}
	if v := readSkillVersion(claude.New()); v != wantVersion {
		t.Errorf("VERSION sidecar=%q, want %q", v, wantVersion)
	}
}

func TestSkillRefreshNeverDowngrades(t *testing.T) {
	cases := []struct {
		name      string
		installed string
		binary    string
		refresh   bool
	}{
		{"dev install, release binary", "dev", "v0.1.0-alpha.39", false},
		{"newer install, older release binary", "v0.1.0-alpha.40", "v0.1.0-alpha.39", false},
		{"older install, newer release binary", "v0.1.0-alpha.39", "v0.1.0-alpha.40", true},
		{"equal versions", "v0.1.0-alpha.39", "v0.1.0-alpha.39", false},
		{"source-build install of same tag", "v0.1.0-alpha.39-3-gabc1234", "v0.1.0-alpha.39", false},
		{"source-build install of older tag", "v0.1.0-alpha.38-3-gabc1234", "v0.1.0-alpha.39", true},
		{"unparsable install", "garbage", "v0.1.0-alpha.39", false},
		{"dev binary over release install", "v0.1.0-alpha.39", "dev", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(selfUpgradeEnv, "")
			skillPath, settingsPath := installSkillAs(t, tc.installed)
			Version = tc.binary
			maybeAutoUpgradeSkill()
			if tc.refresh {
				assertRefreshed(t, skillPath, settingsPath, tc.binary)
			} else {
				assertUntouched(t, skillPath, settingsPath, tc.installed)
			}
		})
	}
}

// A pre-sidecar install (SKILL.md present, no VERSION file) keeps the
// historical behavior: a release binary refreshes it.
func TestSkillRefreshLegacyInstallWithoutSidecar(t *testing.T) {
	t.Setenv(selfUpgradeEnv, "")
	skillPath, settingsPath := installSkillAs(t, "v0.1.0-alpha.1")
	p, _ := claude.New().SkillVersionPath()
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	Version = "v0.1.0-alpha.39"
	maybeAutoUpgradeSkill()
	assertRefreshed(t, skillPath, settingsPath, "v0.1.0-alpha.39")
}

// The incident: a repro set FLOW_NO_AUTO_UPGRADE=1 and still had its
// hooks swapped. That switch now also stops the implicit refresh.
func TestSkillRefreshDisabledByNoAutoUpgrade(t *testing.T) {
	skillPath, settingsPath := installSkillAs(t, "v0.1.0-alpha.39")
	t.Setenv(selfUpgradeEnv, "1")
	Version = "v0.1.0-alpha.40"
	maybeAutoUpgradeSkill()
	assertUntouched(t, skillPath, settingsPath, "v0.1.0-alpha.39")
}

// `flow init` with an existing skill must not let an older binary
// overwrite a newer install's hooks.
func TestInitDoesNotDowngradeHooks(t *testing.T) {
	initTempFlowRoot(t)
	withVersion(t, "v0.1.0-alpha.40")
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	settingsPath := filepath.Join(os.Getenv("HOME"), ".claude", "skills", "flow", "hooks", "hooks.json")
	if err := os.WriteFile(settingsPath, []byte(`{"sentinel":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	Version = "v0.1.0-alpha.39"
	stderr := stderrOf(t, func() {
		if rc := cmdInit([]string{"--harness", "claude"}); rc != 0 {
			t.Fatalf("init rc=%d", rc)
		}
	})
	if got, _ := os.ReadFile(settingsPath); string(got) != `{"sentinel":true}` {
		t.Errorf("init by an older binary rewrote hooks; hooks.json=%s", got)
	}
	if !strings.Contains(stderr, "newer") {
		t.Errorf("want a warning about the newer install; stderr=%q", stderr)
	}
}

// Explicit `flow skill update` is the user's deliberate choice: it
// proceeds, but warns when it replaces a newer install.
func TestSkillUpdateWarnsOnDowngrade(t *testing.T) {
	skillPath, _ := installSkillAs(t, "v0.1.0-alpha.40")
	Version = "v0.1.0-alpha.39"
	stderr := stderrOf(t, func() {
		if rc := cmdSkill([]string{"update"}); rc != 0 {
			t.Fatalf("update rc=%d", rc)
		}
	})
	if got, _ := os.ReadFile(skillPath); string(got) == "sentinel-skill" {
		t.Error("explicit update did not install")
	}
	if !strings.Contains(stderr, "v0.1.0-alpha.40") || !strings.Contains(stderr, "newer") {
		t.Errorf("want a downgrade warning naming the newer version; stderr=%q", stderr)
	}
}

func TestSkillUpdateNoWarningOnUpgrade(t *testing.T) {
	installSkillAs(t, "v0.1.0-alpha.39")
	Version = "v0.1.0-alpha.40"
	stderr := stderrOf(t, func() {
		if rc := cmdSkill([]string{"update"}); rc != 0 {
			t.Fatalf("update rc=%d", rc)
		}
	})
	if strings.Contains(stderr, "newer") {
		t.Errorf("unexpected downgrade warning on an upgrade; stderr=%q", stderr)
	}
}
