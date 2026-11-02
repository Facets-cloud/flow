package app

import (
	"strings"
	"testing"

	"flow/internal/flowdb"
)

// The listener recipe must match Claude Code's real Monitor tool: there
// is no `persistent` param, timeout_ms is required, and an expired
// monitor must be re-armed.
func assertMonitorRecipe(t *testing.T, where, text string) {
	t.Helper()
	if strings.Contains(text, "persistent: true") || strings.Contains(text, "persistent Monitor") {
		t.Errorf("%s still advertises a persistent Monitor", where)
	}
	if strings.Contains(text, "no re-arming") || strings.Contains(text, "NO re-arming") {
		t.Errorf("%s still says no re-arming", where)
	}
	for _, want := range []string{"timeout_ms: 1800000", "re-arm it immediately", "select:Monitor"} {
		if !strings.Contains(text, want) {
			t.Errorf("%s missing %q", where, want)
		}
	}
}

func TestBusSessionStartContextMonitorRecipe(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "Parent"}); rc != 0 {
		t.Fatal("add task")
	}
	bindSession(t, "parent", "sid-p")
	assertMonitorRecipe(t, "SessionStart hook context", busSessionStartContext())
}

func TestSkillMonitorRecipe(t *testing.T) {
	assertMonitorRecipe(t, "skill corpus", skillCorpus(t))
}

func TestAddTaskBoundParentWatchesChild(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "Modelling"}); rc != 0 {
		t.Fatal("add parent")
	}
	bindSession(t, "modelling", "sid-parent")

	var rc int
	out := captureStdout(t, func() { rc = cmdAdd([]string{"task", "Child Audit"}) })
	if rc != 0 {
		t.Fatalf("add child rc=%d", rc)
	}
	watches, err := flowdb.ListWatches(openFlowDB(t), "user/modelling")
	if err != nil {
		t.Fatal(err)
	}
	if len(watches) != 1 || watches[0] != "child-audit" {
		t.Errorf("watches of user/modelling = %v, want [child-audit]", watches)
	}
	if !strings.Contains(out, "(user/modelling now watches child-audit)") {
		t.Errorf("add output should announce the watch, got:\n%s", out)
	}
}

func TestAddTaskUnboundCreatesNoWatch(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "")
	var rc int
	out := captureStdout(t, func() { rc = cmdAdd([]string{"task", "Solo"}) })
	if rc != 0 {
		t.Fatal("add task")
	}
	var n int
	if err := openFlowDB(t).QueryRow(`SELECT COUNT(*) FROM bus_watches`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("unbound add created %d watch rows, want 0", n)
	}
	if strings.Contains(out, "now watches") {
		t.Errorf("unbound add should not announce a watch, got:\n%s", out)
	}
}

func TestAutoPromptReportsToParent(t *testing.T) {
	p := buildAutoBootstrapPrompt("child", "regular", "", "papa")
	if !strings.Contains(p, "flow message user/papa") {
		t.Errorf("auto prompt with parent should tell the child to message user/papa:\n%s", p)
	}
	if !strings.Contains(p, "flow done child") {
		t.Errorf("auto prompt must still require flow done")
	}
	orphan := buildAutoBootstrapPrompt("child", "regular", "", "")
	if strings.Contains(orphan, "flow message user/") {
		t.Errorf("parentless auto prompt must not mention messaging a parent:\n%s", orphan)
	}
}

func TestDoAutoPrintsParentListenerReminder(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "Papa"}); rc != 0 {
		t.Fatal("add parent")
	}
	bindSession(t, "papa", "sid-papa")
	if rc := cmdAdd([]string{"task", "Kid"}); rc != 0 {
		t.Fatal("add child")
	}
	stubITerm(t)
	stubAutoLauncher(t, 4242)

	var rc int
	out := captureStdout(t, func() { rc = cmdDo([]string{"kid", "--auto"}) })
	if rc != 0 {
		t.Fatalf("do --auto rc=%d", rc)
	}
	for _, want := range []string{"user/papa is watching kid", "Monitor"} {
		if !strings.Contains(out, want) {
			t.Errorf("do --auto output missing %q, got:\n%s", want, out)
		}
	}

	// Unwatched: the reminder says so.
	if _, err := flowdb.RemoveWatch(openFlowDB(t), "user/papa", "kid"); err != nil {
		t.Fatal(err)
	}
	if rc := cmdAdd([]string{"task", "Kid Two"}); rc != 0 {
		t.Fatal("add child two")
	}
	if _, err := flowdb.RemoveWatch(openFlowDB(t), "user/papa", "kid-two"); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() { rc = cmdDo([]string{"kid-two", "--auto"}) })
	if rc != 0 {
		t.Fatalf("do --auto rc=%d", rc)
	}
	if !strings.Contains(out, "user/papa is NOT watching kid-two") {
		t.Errorf("unwatched reminder missing, got:\n%s", out)
	}
}

func TestDoAutoParentlessNoReminder(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "")
	seedTask(t, "lone")
	stubITerm(t)
	stubAutoLauncher(t, 4242)
	out := captureStdout(t, func() { cmdDo([]string{"lone", "--auto"}) })
	if strings.Contains(out, "watching") {
		t.Errorf("parentless do --auto should not print a parent reminder, got:\n%s", out)
	}
}
