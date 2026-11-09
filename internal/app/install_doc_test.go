package app

import (
	"io/fs"
	"os"
	"strings"
	"testing"
)

// TestInstallDocIsAgentReady pins the shape of INSTALL.md — the file a
// coding agent follows when the user pastes "Install flow from
// https://github.com/Facets-cloud/flow". It must name the one-line
// installer, the PATH check, `flow init`, both verification commands,
// both OSes, and the sudo rule, and stay short enough to be followed
// verbatim.
func TestInstallDocIsAgentReady(t *testing.T) {
	b, err := os.ReadFile("../../INSTALL.md")
	if err != nil {
		t.Fatalf("INSTALL.md must exist at the repo root: %v", err)
	}
	doc := string(b)
	for _, want := range []string{
		"https://raw.githubusercontent.com/Facets-cloud/flow/main/install.sh",
		"uname -s",
		"PATH",
		"flow init",
		"flow --version",
		"flow list tasks",
		"macOS",
		"Linux",
		"sudo",
		"brew install facets-cloud/tap/flow",
		"tmux",
		"FLOW_INSTALL_DIR",
		"FLOW_VERSION",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("INSTALL.md lacks %q", want)
		}
	}
	// Agent docs drift into essays; keep this one a checklist.
	if n := strings.Count(doc, "\n"); n > 140 {
		t.Errorf("INSTALL.md is %d lines; keep it under 140 so an agent follows it verbatim", n)
	}
	// Both OSes use the same path; the doc must not tell Linux users to
	// run a macOS-only command unconditionally.
	if strings.Contains(doc, "xattr") && !strings.Contains(doc, "macOS only") && !strings.Contains(doc, "macOS:") {
		t.Errorf("INSTALL.md mentions xattr without scoping it to macOS")
	}
}

// TestSkillUpgradeReferenceIsOSAware — the skill's upgrade workflow must
// route through INSTALL.md (same procedure on macOS and Linux) instead of
// hard-coding the macOS-only xattr step and the README.
func TestSkillUpgradeReferenceIsOSAware(t *testing.T) {
	ref := skillCorpusFile(t, "references/upgrade.md")
	for _, want := range []string{"INSTALL.md", "Linux", "macOS", "install.sh", "flow skill update", "brew upgrade"} {
		if !strings.Contains(ref, want) {
			t.Errorf("references/upgrade.md lacks %q", want)
		}
	}
	if strings.Contains(ref, "Do not skip the `xattr") {
		t.Errorf("references/upgrade.md still makes xattr unconditional; it is macOS-only and install.sh already does it")
	}
}

// TestSkillKnowsLinuxTerminals — the skill corpus must mention the tmux
// backend and the no-terminal handoff so a Linux session relays the
// printed command instead of treating it as a failure.
func TestSkillKnowsLinuxTerminals(t *testing.T) {
	corpus := skillCorpus(t)
	for _, want := range []string{"tmux", "no supported terminal", "flow do --here", "flow do --auto"} {
		if !strings.Contains(corpus, want) {
			t.Errorf("skill corpus lacks %q", want)
		}
	}
	core := skillCorpusFile(t, "SKILL.md")
	if !strings.Contains(core, "no supported terminal") {
		t.Errorf("SKILL.md core must name the no-terminal handoff so the trigger is resident")
	}
}

// skillCorpusFile returns one file of the embedded skill tree.
func skillCorpusFile(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(skillFiles(), name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
