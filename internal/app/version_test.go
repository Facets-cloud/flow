package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/harness/opencode"
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

// TestMaybeAutoUpgradeRefreshesInstalledOpencode pins the secondary-harness
// upgrade: an already-installed opencode (VERSION sidecar present) whose
// recorded version is stale gets its skill tree and plugin refreshed by a
// plain flow invocation.
func TestMaybeAutoUpgradeRefreshesInstalledOpencode(t *testing.T) {
	home := withTempHome(t)
	withVersion(t, "v1.0.0")
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}

	h := opencode.New()
	versionPath, err := h.SkillVersionPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(versionPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(versionPath, []byte("v0.9.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	Version = "v2.0.0"
	maybeAutoUpgradeSkill()

	skillPath := filepath.Join(home, ".config", "opencode", "skills", "flow", "SKILL.md")
	if b, err := os.ReadFile(skillPath); err != nil || len(b) == 0 {
		t.Errorf("opencode SKILL.md not refreshed at %s: %v", skillPath, err)
	}
	pluginPath := filepath.Join(home, ".config", "opencode", "plugins", "flow", "index.js")
	if b, err := os.ReadFile(pluginPath); err != nil || len(b) == 0 {
		t.Errorf("opencode plugin not refreshed at %s: %v", pluginPath, err)
	}
	if b, err := os.ReadFile(versionPath); err != nil || strings.TrimSpace(string(b)) != "v2.0.0" {
		t.Errorf("opencode VERSION = %q (err=%v), want v2.0.0", string(b), err)
	}
}

// TestMaybeAutoUpgradeSkipsUninstalledOpencode pins the opt-out rule: a
// missing opencode VERSION sidecar means the user never installed flow for
// opencode, so nothing may be written under ~/.config/opencode.
func TestMaybeAutoUpgradeSkipsUninstalledOpencode(t *testing.T) {
	home := withTempHome(t)
	withVersion(t, "v2.0.0")
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}

	maybeAutoUpgradeSkill()

	ocDir := filepath.Join(home, ".config", "opencode")
	if _, err := os.Stat(ocDir); !os.IsNotExist(err) {
		t.Errorf("auto-upgrade created %s without an existing VERSION sidecar; err=%v", ocDir, err)
	}
}
