package app

import (
	"os"
	"path/filepath"
	"testing"
)

// withVersion temporarily overrides the package-level Version for the
// duration of a test.
func withVersion(t *testing.T, v string) {
	t.Helper()
	old := Version
	Version = v
	t.Cleanup(func() { Version = old })
}

func TestSkillInstallWritesVersionSidecar(t *testing.T) {
	home := withTempHome(t)
	withVersion(t, "v9.9.9")

	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	got, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "flow", "VERSION"))
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	if want := "v9.9.9\n"; string(got) != want {
		t.Errorf("VERSION sidecar = %q, want %q", got, want)
	}
}

func TestCmdInitWritesVersionSidecar(t *testing.T) {
	initTempFlowRoot(t)
	withVersion(t, "v1.2.3")

	if rc := cmdInit(nil); rc != 0 {
		t.Fatalf("cmdInit rc=%d", rc)
	}
	if got := readSkillVersion(); got != "v1.2.3" {
		t.Errorf("readSkillVersion=%q, want v1.2.3", got)
	}
}

func TestMaybeAutoUpgradeUpgradesOnMismatch(t *testing.T) {
	home := withTempHome(t)
	withVersion(t, "v1.0.0")
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	// Stomp on the on-disk skill so we can detect the refresh.
	skillPath := filepath.Join(home, ".claude", "skills", "flow", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Bump binary version → auto-upgrade should trigger.
	Version = "v2.0.0"
	maybeAutoUpgradeSkill()

	got, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == "stale" {
		t.Error("auto-upgrade did not refresh SKILL.md")
	}
	if v := readSkillVersion(); v != "v2.0.0" {
		t.Errorf("VERSION sidecar=%q after upgrade, want v2.0.0", v)
	}
}

func TestMaybeAutoUpgradeIdempotent(t *testing.T) {
	home := withTempHome(t)
	withVersion(t, "v1.0.0")
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	skillPath := filepath.Join(home, ".claude", "skills", "flow", "SKILL.md")
	before, _ := os.Stat(skillPath)

	// Same version → no rewrite.
	maybeAutoUpgradeSkill()

	after, err := os.Stat(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("auto-upgrade rewrote SKILL.md when version was unchanged")
	}
}

func TestMaybeAutoUpgradeSkipsForDev(t *testing.T) {
	home := withTempHome(t)
	withVersion(t, "v1.0.0")
	if rc := cmdSkill([]string{"install"}); rc != 0 {
		t.Fatalf("install rc=%d", rc)
	}
	skillPath := filepath.Join(home, ".claude", "skills", "flow", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("dev edits"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Dev build → must not touch user-edited skill.
	Version = "dev"
	maybeAutoUpgradeSkill()

	got, _ := os.ReadFile(skillPath)
	if string(got) != "dev edits" {
		t.Error("dev build clobbered locally-edited SKILL.md")
	}
}

func TestMaybeAutoUpgradeSkipsWhenSkillMissing(t *testing.T) {
	withTempHome(t)
	withVersion(t, "v2.0.0")

	// No install, no skill on disk → should be a no-op.
	maybeAutoUpgradeSkill()

	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".claude", "skills", "flow", "SKILL.md")); !os.IsNotExist(err) {
		t.Errorf("auto-upgrade created a skill file when none existed; err=%v", err)
	}
}
