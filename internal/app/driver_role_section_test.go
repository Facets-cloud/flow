package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The driver role lives in the task's brief.md as a `## Role: driver`
// section (flow #58). These tests cover insertion on becoming a driver,
// idempotence, preservation of a user-edited section, and the hook
// switching between the short pointer and the full fallback block.

const roleHeading = "## Role: driver"

// fallbackOnlyPhrase appears in the full fallback block but not in the
// pointer, so it tells the two apart.
const fallbackOnlyPhrase = "Needs from you"

func briefOf(t *testing.T, slug string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(os.Getenv("FLOW_ROOT"), "tasks", slug, "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeTaskBrief(t *testing.T, slug, body string) {
	t.Helper()
	p := filepath.Join(os.Getenv("FLOW_ROOT"), "tasks", slug, "brief.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAddTaskTagDriverAppendsRoleSection(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "drv", "--tag", "driver"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	b := briefOf(t, "drv")
	if n := strings.Count(b, roleHeading); n != 1 {
		t.Fatalf("want exactly one %q, got %d:\n%s", roleHeading, n, b)
	}
	if !strings.HasSuffix(strings.TrimSpace(b), strings.TrimSpace(driverRoleSection)) {
		t.Errorf("Role section should be the last section:\n%s", b)
	}
}

func TestAddTaskWithoutDriverTagHasNoRoleSection(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "plain", "--tag", "frontend"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if strings.Contains(briefOf(t, "plain"), "## Role") {
		t.Error("a non-driver task must not get a Role section")
	}
}

func TestUpdateTagDriverAppendsRoleLastAndOnce(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "mid")
	writeTaskBrief(t, "mid", "# mid\n\n## What\nx\n\n## Done when\ny\n\n## Open questions\nz\n")
	for _, args := range [][]string{
		{"task", "mid", "--tag", "driver"},
		{"task", "mid", "--tag", "driver"}, // re-tag: already a driver
		{"task", "mid", "--remove-tag", "driver"},
		{"task", "mid", "--tag", "driver"}, // retag after removal
	} {
		if rc := cmdUpdate(args); rc != 0 {
			t.Fatalf("%v rc=%d", args, rc)
		}
	}
	b := briefOf(t, "mid")
	if n := strings.Count(b, roleHeading); n != 1 {
		t.Fatalf("want exactly one %q, got %d:\n%s", roleHeading, n, b)
	}
	if strings.Index(b, roleHeading) < strings.Index(b, "## Open questions") {
		t.Errorf("Role must come after Open questions:\n%s", b)
	}
	if !strings.HasPrefix(b, "# mid\n\n## What\nx\n\n## Done when\ny\n\n## Open questions\nz\n") {
		t.Errorf("existing brief content must be untouched:\n%s", b)
	}
	if !strings.HasSuffix(strings.TrimSpace(b), strings.TrimSpace(driverRoleSection)) {
		t.Errorf("Role section should be the last section:\n%s", b)
	}
}

func TestExistingRoleSectionIsPreserved(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "own")
	custom := "# own\n\n## Role: driver\nMy own edited role text.\n\n## Notes\nkeep\n"
	writeTaskBrief(t, "own", custom)
	if rc := cmdUpdate([]string{"task", "own", "--tag", "driver"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if got := briefOf(t, "own"); got != custom {
		t.Errorf("an existing Role section must not be overwritten or duplicated:\n%s", got)
	}
}

func TestSessionStartDriverWithRoleSectionGetsPointer(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	if rc := cmdAdd([]string{"task", "drv", "--tag", "driver"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	bindSession(t, "drv", "sid-drv")
	out := sessionStartOutput(t)
	if !strings.Contains(out, "follow the ## Role section in your brief.md") {
		t.Errorf("driver with a Role section should get the pointer:\n%s", out)
	}
	if strings.Contains(out, fallbackOnlyPhrase) {
		t.Errorf("pointer case must not repeat the full block:\n%s", out)
	}
}

func TestSessionStartDriverWithoutRoleSectionGetsFallback(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	seedTask(t, "old")
	tagTask(t, "old", "driver") // tagged directly: brief has no Role section
	bindSession(t, "old", "sid-old")
	out := sessionStartOutput(t)
	if !strings.Contains(out, fallbackOnlyPhrase) {
		t.Errorf("driver without a Role section should get the full fallback block:\n%s", out)
	}
}

func TestDoHereDriverWithRoleSectionPrintsPointer(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	cwd, _ := os.Getwd()
	if rc := cmdAdd([]string{"task", "here-drv", "--work-dir", cwd, "--tag", "driver"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	stubClaudeStatOK(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "f00ba111-2222-4333-8444-555555555556")
	stubITerm(t)
	var rc int
	out := captureStdout(t, func() { rc = cmdDo([]string{"here-drv", "--here"}) })
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(out, "follow the ## Role section in your brief.md") {
		t.Errorf("--here should print the pointer:\n%s", out)
	}
	if strings.Contains(out, fallbackOnlyPhrase) {
		t.Errorf("--here pointer case must not print the full block:\n%s", out)
	}
}

func TestUpdateTagDriverOnBoundTaskPrintsPointer(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	seedTask(t, "bnd")
	bindSession(t, "bnd", "sid-bnd")
	out := captureStdout(t, func() { cmdUpdate([]string{"task", "bnd", "--tag", "driver"}) })
	if !strings.Contains(out, "follow the ## Role section in your brief.md") {
		t.Errorf("tagging the bound task should print the pointer (Role now in brief):\n%s", out)
	}
}

func TestAutoChildWithRoleSectionGetsNoBlock(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "kid", "--tag", "driver"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	bindSession(t, "kid", "sid-kid")
	t.Setenv("FLOW_AUTO_RUN", "kid")
	if out := sessionStartOutput(t); strings.Contains(out, driverRoleMarker) {
		t.Errorf("auto child must never get a driver block:\n%s", out)
	}
}

// The Role template in references/templates.md is the same text flow
// appends, so the skill and the binary cannot drift.
func TestRoleTemplateMatchesTemplatesReference(t *testing.T) {
	ref, err := fs.ReadFile(skillFiles(), "references/templates.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ref), strings.TrimSpace(driverRoleSection)) {
		t.Errorf("references/templates.md must carry the Role section verbatim:\n%s", driverRoleSection)
	}
}

func TestRoleSectionContent(t *testing.T) {
	s := driverRoleSection
	if !strings.HasPrefix(strings.TrimSpace(s), roleHeading) {
		t.Errorf("Role section must start with %q", roleHeading)
	}
	for _, want := range []string{
		"end to end", "Done when", "verified", "flow auto",
		"output file", "gates", "NOT", "never block waiting for a reply",
		"flow do --auto <slug> --with", "newer HOLD beats an older GO",
		"the way a dev lead would", "never AskUserQuestion", "close",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Role section missing %q", want)
		}
	}
	if strings.ContainsRune(s, '\u2014') {
		t.Error("Role section contains an em dash")
	}
}
