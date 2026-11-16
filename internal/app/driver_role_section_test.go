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
	// A brief the hook cannot heal keeps the full fallback. brief.md is
	// replaced by a directory: unreadable and unwritable as a file for
	// every user, root included (a 0444 chmod is not, under
	// CAP_DAC_OVERRIDE).
	p := filepath.Join(os.Getenv("FLOW_ROOT"), "tasks", "old", "brief.md")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o755); err != nil {
		t.Fatal(err)
	}
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

// --- Round 2: self-heal + fenced headings -------------------------------

// rewriteWithoutRole simulates §4.2 intake overwriting a driver's stub
// with a drafted brief that dropped the Role section.
const rewrittenBrief = "# drv\n\n## What\nx\n\n## Open questions\nz\n"

func newDriverWithRewrittenBrief(t *testing.T, slug string) {
	t.Helper()
	cwd, _ := os.Getwd()
	if rc := cmdAdd([]string{"task", slug, "--work-dir", cwd, "--tag", "driver"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	writeTaskBrief(t, slug, rewrittenBrief)
}

func assertHealedOnce(t *testing.T, slug string) {
	t.Helper()
	b := briefOf(t, slug)
	if n := strings.Count(b, roleHeading); n != 1 {
		t.Fatalf("want exactly one %q after self-heal, got %d:\n%s", roleHeading, n, b)
	}
	if !strings.HasPrefix(b, rewrittenBrief) {
		t.Errorf("self-heal must not touch existing content:\n%s", b)
	}
	if !strings.HasSuffix(strings.TrimSpace(b), strings.TrimSpace(driverRoleSection)) {
		t.Errorf("healed Role section must be last:\n%s", b)
	}
}

func TestSessionStartReappendsLostRoleSection(t *testing.T) {
	setupFlowRoot(t)
	newDriverWithRewrittenBrief(t, "drv")
	bindSession(t, "drv", "sid-drv")
	out := sessionStartOutput(t)
	sessionStartOutput(t) // a resume must not append a second copy
	assertHealedOnce(t, "drv")
	if !strings.Contains(out, "follow the ## Role section in your brief.md") {
		t.Errorf("after self-heal the hook should point at the section:\n%s", out)
	}
}

func TestDoReappendsLostRoleSection(t *testing.T) {
	setupFlowRoot(t)
	newDriverWithRewrittenBrief(t, "drv")
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}
	stubClaudeStatOK(t)
	stubITerm(t)
	captureStdout(t, func() {
		if rc := cmdDo([]string{"drv"}); rc != 0 {
			t.Fatalf("cmdDo rc=%d", rc)
		}
	})
	assertHealedOnce(t, "drv")
}

func TestDoHereReappendsLostRoleSection(t *testing.T) {
	setupFlowRoot(t)
	newDriverWithRewrittenBrief(t, "drv")
	stubClaudeStatOK(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "f00ba111-2222-4333-8444-555555555557")
	stubITerm(t)
	out := captureStdout(t, func() {
		if rc := cmdDo([]string{"drv", "--here"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	assertHealedOnce(t, "drv")
	if !strings.Contains(out, "follow the ## Role section in your brief.md") {
		t.Errorf("--here should point at the healed section:\n%s", out)
	}
}

func TestSelfHealKeepsExistingRoleAndSkipsNonDrivers(t *testing.T) {
	setupFlowRoot(t)
	// Existing (custom) Role section on a driver: untouched.
	newDriverWithRewrittenBrief(t, "own")
	custom := "# own\n\n## Role: driver\nmine\n"
	writeTaskBrief(t, "own", custom)
	bindSession(t, "own", "sid-own")
	sessionStartOutput(t)
	if got := briefOf(t, "own"); got != custom {
		t.Errorf("existing Role section must be kept as is:\n%s", got)
	}
	// Non-driver: never gets a Role section.
	seedTask(t, "plain")
	writeTaskBrief(t, "plain", rewrittenBrief)
	bindSession(t, "plain", "sid-plain")
	sessionStartOutput(t)
	if got := briefOf(t, "plain"); got != rewrittenBrief {
		t.Errorf("non-driver brief must be untouched:\n%s", got)
	}
}

func TestSelfHealSkipsAutoChild(t *testing.T) {
	setupFlowRoot(t)
	newDriverWithRewrittenBrief(t, "kid")
	bindSession(t, "kid", "sid-kid")
	t.Setenv("FLOW_AUTO_RUN", "kid")
	sessionStartOutput(t)
	if got := briefOf(t, "kid"); got != rewrittenBrief {
		t.Errorf("auto child's brief must be untouched by the hook:\n%s", got)
	}
	t.Setenv("FLOW_AUTO_RUN", "")
	stubAutoLauncher(t, 4242)
	captureStdout(t, func() { cmdDo([]string{"kid", "--auto"}) })
	if got := briefOf(t, "kid"); got != rewrittenBrief {
		t.Errorf("flow do --auto must not touch the brief:\n%s", got)
	}
}

// CodeRabbit: a `## Role` heading inside a fenced code block is an
// example, not the task's Role section.
const fencedRoleBrief = "# drv\n\n## What\nExample child brief:\n\n```markdown\n## Role: reviewer\nreview only\n```\n\n~~~\n## Role\n~~~\n"

func TestFencedRoleHeadingDoesNotCount(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "drv")
	writeTaskBrief(t, "drv", fencedRoleBrief)
	tagTask(t, "drv", "driver") // DB-only: brief still lacks a real Role
	if briefHasRoleSection("drv") {
		t.Fatal("fenced ## Role example must not count as a Role section")
	}
	if !strings.Contains(driverRoleBlockFor("drv"), fallbackOnlyPhrase) {
		t.Error("without a real Role section the fallback block must be chosen")
	}
	if err := ensureDriverRoleSection("drv"); err != nil {
		t.Fatal(err)
	}
	b := briefOf(t, "drv")
	if !strings.HasPrefix(b, fencedRoleBrief) || strings.Count(b, roleHeading) != 1 {
		t.Fatalf("Role section should be appended after the fenced example:\n%s", b)
	}
	if !briefHasRoleSection("drv") ||
		!strings.Contains(driverRoleBlockFor("drv"), "follow the ## Role section in your brief.md") {
		t.Error("after insertion the pointer must be chosen")
	}
}

// CodeRabbit: a Role section left behind after untagging must say it is
// inactive, in both the section itself and delegation.md.
func TestRetainedRoleSectionIsMarkedInactiveWithoutTag(t *testing.T) {
	if !strings.Contains(driverRoleSection, "only while this task is tagged `driver`") {
		t.Error("Role section must say it applies only while the task is tagged driver")
	}
	ref, err := fs.ReadFile(skillFiles(), "references/delegation.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ref), "is ignored once the `driver` tag is removed") {
		t.Error("delegation.md must say a retained Role section is ignored once the driver tag is removed")
	}
}

// --- Round 3: CommonMark heading indent + unclosed fences ---------------

func TestRoleHeadingIndentFollowsCommonMark(t *testing.T) {
	for _, tc := range []struct {
		brief string
		want  bool
	}{
		{"# t\n## Role: driver\n", true},
		{"# t\n ## Role: driver\n", true},
		{"# t\n   ## Role: driver\n", true},
		{"# t\n    ## Role: driver\n", false},                     // 4 spaces: indented code
		{"# t\n\t## Role: driver\n", false},                       // tab: indented code
		{"# t\n    ```\n## Role: driver\n", true},                 // indented ``` is code, not a fence
		{"```markdown\n\t```\n## Role: reviewer\n```\n", false},   // tab-indented closer does not close
		{"```markdown\n    ```\n## Role: reviewer\n```\n", false}, // 4-space closer does not close
		{"```markdown\n   ```\n## Role: driver\n", true},          // 3-space closer closes
		{"```example `name`\n\n## Role: reviewer\n", true},        // backtick in info string: not a fence
		{"~~~example `name`\n## Role: reviewer\n~~~\n", false},    // tilde info strings unrestricted
		{"```go\n## Role\n```` \n## Role: driver\n", true},        // longer closer + trailing space closes
	} {
		if got := hasRoleHeading([]byte(tc.brief)); got != tc.want {
			t.Errorf("hasRoleHeading(%q) = %v, want %v", tc.brief, got, tc.want)
		}
	}
}

func TestUnclosedFenceIsClosedBeforeAppendingRole(t *testing.T) {
	for _, tail := range []string{"```markdown\n## Role: reviewer\nexample", "~~~~\nexample\n"} {
		t.Run(tail[:3], func(t *testing.T) {
			setupFlowRoot(t)
			seedTask(t, "drv")
			body := "# drv\n\n## What\nx\n\n" + tail
			writeTaskBrief(t, "drv", body)
			tagTask(t, "drv", "driver")
			db := openFlowDB(t)
			for i := 0; i < 3; i++ { // repeated healing must append once
				healDriverRoleSection(db, "drv", false)
			}
			b := briefOf(t, "drv")
			if n := strings.Count(b, roleHeading); n != 1 {
				t.Fatalf("want exactly one %q after repeated healing, got %d:\n%s", roleHeading, n, b)
			}
			if !strings.HasPrefix(b, body) {
				t.Errorf("existing content must be kept:\n%s", b)
			}
			if !briefHasRoleSection("drv") {
				t.Errorf("appended Role must sit outside the fence:\n%s", b)
			}
			if !strings.HasSuffix(strings.TrimSpace(b), strings.TrimSpace(driverRoleSection)) {
				t.Errorf("Role must be the last section:\n%s", b)
			}
		})
	}
}

func TestBacktickInfoStringBriefWithRoleIsUnchanged(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "drv")
	custom := "```example `name`\n\n## Role: reviewer\nCustom instructions\n"
	writeTaskBrief(t, "drv", custom)
	tagTask(t, "drv", "driver")
	healDriverRoleSection(openFlowDB(t), "drv", false)
	if got := briefOf(t, "drv"); got != custom {
		t.Errorf("a brief whose Role section follows a non-fence line must stay unchanged:\n%s", got)
	}
}

// --- Round 4: Anuj's review ---------------------------------------------

func TestRoleSectionCarriesDriverHabits(t *testing.T) {
	for _, want := range []string{
		"Children cannot spawn grandchildren.",
		"Don't idle while children run; use the time on open decisions.",
		"End any turn that needs the user with a numbered \"Needs from you:\" list.",
		"In a headless `flow auto` run, do the work yourself; delegation applies only to the interactive driver session.",
	} {
		if !strings.Contains(driverRoleSection, want) {
			t.Errorf("Role section missing %q", want)
		}
	}
}

func TestPlaybookRunBriefIsNeverGivenARole(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "run")
	writeTaskBrief(t, "run", rewrittenBrief)
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET kind='playbook_run' WHERE slug='run'`); err != nil {
		t.Fatal(err)
	}
	// Tagging, flow do, and SessionStart must all leave the frozen snapshot alone.
	if rc := cmdUpdate([]string{"task", "run", "--tag", "driver"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	healDriverRoleSection(db, "run", false)
	bindSession(t, "run", "sid-run")
	sessionStartOutput(t)
	if got := briefOf(t, "run"); got != rewrittenBrief {
		t.Errorf("a playbook_run brief is a frozen snapshot and must be untouched:\n%s", got)
	}
}

func TestRoleHeadingMustBeTheRoleSection(t *testing.T) {
	for _, tc := range []struct {
		brief string
		want  bool
	}{
		{"## Role\n", true},
		{"## Role: driver\n", true},
		{"## Role : driver\n", true},
		{"## Role ##\n", true},
		{"## Role   \n", true},
		{"## Role of this service\n", false},
		{"## Roles\n", false},
		{"## Role-based access\n", false},
	} {
		if got := hasRoleHeading([]byte(tc.brief)); got != tc.want {
			t.Errorf("hasRoleHeading(%q) = %v, want %v", tc.brief, got, tc.want)
		}
	}
}

func TestDoHereAlreadyBoundHeals(t *testing.T) {
	setupFlowRoot(t)
	newDriverWithRewrittenBrief(t, "drv")
	sid := "f00ba111-2222-4333-8444-555555555558"
	bindSession(t, "drv", sid)
	stubClaudeStatOK(t)
	stubITerm(t)
	out := captureStdout(t, func() {
		if rc := cmdDo([]string{"drv", "--here"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "already bound") {
		t.Fatalf("expected the already-bound path:\n%s", out)
	}
	assertHealedOnce(t, "drv")
}
