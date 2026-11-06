package app

import (
	"io/fs"
	"strings"
	"testing"
)

// Bus fixes F1–F10 (human mail hygiene). One block per behavior.

func readSkillFile(t *testing.T, p string) string {
	t.Helper()
	data, err := fs.ReadFile(skillFiles(), p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(data)
}

// skillSection returns the text of a "### <heading>" section of SKILL.md
// up to the next "### " heading.
func skillSection(t *testing.T, heading string) string {
	t.Helper()
	core := readSkillFile(t, "SKILL.md")
	i := strings.Index(core, "### "+heading)
	if i < 0 {
		t.Fatalf("SKILL.md has no section %q", heading)
	}
	rest := core[i+4:]
	if j := strings.Index(rest, "\n### "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// F1: only BOUND sessions arm an inbox Monitor. An unbound (dispatch)
// session runs as the human's identity, so a Monitor there would sit on
// the human's own queue.
func TestF1SkillMonitorIsBoundOnly(t *testing.T) {
	for where, text := range map[string]string{
		"SKILL.md §4.18":          skillSection(t, "4.18"),
		"references/messaging.md": readSkillFile(t, "references/messaging.md"),
	} {
		text = strings.Join(strings.Fields(text), " ")
		if !strings.Contains(text, "BOUND session") {
			t.Errorf("%s must say the inbox Monitor is armed only in a BOUND session", where)
		}
		if !strings.Contains(text, "unbound") {
			t.Errorf("%s must tell unbound sessions not to arm a Monitor", where)
		}
	}
}
