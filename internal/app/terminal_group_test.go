package app

import (
	"flow/internal/flowdb"
	"flow/internal/rex"
	"flow/internal/spawner"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func seedProject(t *testing.T, slug string) {
	t.Helper()
	if rc := cmdAdd([]string{"project", slug, "--work-dir", t.TempDir(), "--slug", slug}); rc != 0 {
		t.Fatalf("seed project rc=%d", rc)
	}
}

func projectGroup(t *testing.T, slug string) (string, bool) {
	t.Helper()
	p, err := flowdb.GetProject(openFlowDB(t), slug)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	return p.TerminalGroup.String, p.TerminalGroup.Valid
}

func TestUpdateProjectTerminalGroupSetAndClear(t *testing.T) {
	setupFlowRoot(t)
	seedProject(t, "tekion-modules")

	if g, ok := projectGroup(t, "tekion-modules"); ok {
		t.Fatalf("new project has terminal group %q; want NULL", g)
	}
	if rc := cmdUpdate([]string{"project", "tekion-modules", "--terminal-group", "  tekion "}); rc != 0 {
		t.Fatalf("set rc=%d", rc)
	}
	if g, ok := projectGroup(t, "tekion-modules"); !ok || g != "tekion" {
		t.Errorf("after set: (%q, %v); want (tekion, true) — value trimmed", g, ok)
	}
	out := captureStdout(t, func() { cmdShow([]string{"project", "tekion-modules"}) })
	if !strings.Contains(out, "terminal:    tekion") {
		t.Errorf("show project missing terminal line:\n%s", out)
	}

	if rc := cmdUpdate([]string{"project", "tekion-modules", "--clear-terminal-group"}); rc != 0 {
		t.Fatalf("clear rc=%d", rc)
	}
	if g, ok := projectGroup(t, "tekion-modules"); ok {
		t.Errorf("after clear: %q; want NULL", g)
	}
	out = captureStdout(t, func() { cmdShow([]string{"project", "tekion-modules"}) })
	if strings.Contains(out, "terminal:") {
		t.Errorf("show project prints terminal line for an unset group:\n%s", out)
	}
}

// TestUpdateProjectTerminalGroupWithPriority — both fields in one call,
// and --priority alone still works as before.
func TestUpdateProjectTerminalGroupWithPriority(t *testing.T) {
	setupFlowRoot(t)
	seedProject(t, "sessionm")
	if rc := cmdUpdate([]string{"project", "sessionm", "--priority", "low", "--terminal-group", "sessionm"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	p, err := flowdb.GetProject(openFlowDB(t), "sessionm")
	if err != nil {
		t.Fatal(err)
	}
	if p.Priority != "low" || p.TerminalGroup.String != "sessionm" {
		t.Errorf("got priority=%q group=%q", p.Priority, p.TerminalGroup.String)
	}
	if rc := cmdUpdate([]string{"project", "sessionm", "--priority", "high"}); rc != 0 {
		t.Fatalf("priority-only rc=%d", rc)
	}
	if g, _ := projectGroup(t, "sessionm"); g != "sessionm" {
		t.Errorf("priority-only update changed the group to %q", g)
	}
}

func TestUpdateProjectTerminalGroupUsageErrors(t *testing.T) {
	setupFlowRoot(t)
	seedProject(t, "p1")
	cases := [][]string{
		{"project", "p1"}, // no field
		{"project", "p1", "--terminal-group", "   "},                         // blank
		{"project", "p1", "--terminal-group", "x", "--clear-terminal-group"}, // both
		{"project", "p1", "--priority", "urgent", "--terminal-group", "x"},   // bad priority
	}
	for _, args := range cases {
		if rc := cmdUpdate(args); rc != 2 {
			t.Errorf("cmdUpdate(%v) rc=%d; want 2", args, rc)
		}
	}
	if _, ok := projectGroup(t, "p1"); ok {
		t.Error("a rejected update must not write the group")
	}
}

// stubRex pins the Rex backend and records every rex CLI call. The
// fake server knows sessions "personal" (current) and "tekion".
func stubRex(t *testing.T) *[][]string {
	t.Helper()
	t.Setenv("REX_SESSION", "session:cur")
	oldOverride := spawner.Override
	spawner.Override = spawner.BackendRex
	t.Cleanup(func() { spawner.Override = oldOverride })

	calls := &[][]string{}
	oldOut, oldRun := rex.RunnerOutput, rex.Runner
	t.Cleanup(func() { rex.RunnerOutput, rex.Runner = oldOut, oldRun })
	rex.RunnerOutput = func(args []string) ([]byte, error) {
		*calls = append(*calls, args)
		switch args[0] {
		case "ls":
			return []byte(`{"sessions":[{"session_id":"session:cur","label":"personal"},{"session_id":"session:tk","label":"tekion"}]}`), nil
		case "window":
			return []byte(`{"window_id":"window:w","block_ids":["block:b"]}`), nil
		case "client":
			return []byte(`{"clients":[]}`), nil
		}
		return []byte(`{}`), nil
	}
	rex.Runner = func(args []string) error { *calls = append(*calls, args); return nil }

	// The bootstrap prompt is long enough to go through a spawn script.
	// With rex stubbed nothing ever sources (and so deletes) it; keep it
	// in the test's temp dir instead of the real $TMPDIR.
	dir := t.TempDir()
	oldPath := rex.NewScriptPath
	t.Cleanup(func() { rex.NewScriptPath = oldPath })
	n := 0
	rex.NewScriptPath = func() (string, error) {
		n++
		return filepath.Join(dir, fmt.Sprintf("spawn-%d.sh", n)), nil
	}
	return calls
}

func windowNewSession(calls [][]string) string {
	for _, c := range calls {
		if len(c) > 1 && c[0] == "window" && c[1] == "new" {
			for i, a := range c {
				if a == "-s" && i+1 < len(c) {
					return c[i+1]
				}
			}
			return ""
		}
	}
	return "<no window new>"
}

// TestCmdDoOpensTabInProjectTerminalGroup — `flow do` on a task whose
// project has a terminal group opens the tab in that Rex session, even
// though flow runs from another one.
func TestCmdDoOpensTabInProjectTerminalGroup(t *testing.T) {
	setupFlowRoot(t)
	seedProject(t, "tekion-modules")
	if rc := cmdUpdate([]string{"project", "tekion-modules", "--terminal-group", "tekion"}); rc != 0 {
		t.Fatalf("set group rc=%d", rc)
	}
	if rc := cmdAdd([]string{"task", "tk-task", "--project", "tekion-modules"}); rc != 0 {
		t.Fatalf("add task rc=%d", rc)
	}
	calls := stubRex(t)

	if rc := cmdDo([]string{"tk-task"}); rc != 0 {
		t.Fatalf("do rc=%d", rc)
	}
	if got := windowNewSession(*calls); got != "session:tk" {
		t.Errorf("tab opened in %q; want session:tk (the tekion group)", got)
	}
}

// TestCmdDoWithoutTerminalGroupUsesCurrentSession — ungrouped projects
// and floating tasks keep the old behaviour: no session lookup, tab in
// the session flow ran from.
func TestCmdDoWithoutTerminalGroupUsesCurrentSession(t *testing.T) {
	setupFlowRoot(t)
	seedProject(t, "plain")
	if rc := cmdAdd([]string{"task", "plain-task", "--project", "plain"}); rc != 0 {
		t.Fatalf("add task rc=%d", rc)
	}
	seedTask(t, "floating-task")

	for _, slug := range []string{"plain-task", "floating-task"} {
		calls := stubRex(t)
		if rc := cmdDo([]string{slug}); rc != 0 {
			t.Fatalf("do %s rc=%d", slug, rc)
		}
		if got := windowNewSession(*calls); got != "session:cur" {
			t.Errorf("%s: tab opened in %q; want session:cur", slug, got)
		}
		for _, c := range *calls {
			if c[0] == "ls" {
				t.Errorf("%s: ungrouped spawn must not look up sessions", slug)
			}
		}
	}
}

// TestCmdDoTabTitleFormat — `flow do` titles the tab per FLOW_TAB_TITLE:
// project/task by default (Rex included), the bare slug with "task" (any
// case, surrounding space ignored), and project/task for unknown values.
// Each case uses its own task so every run takes the fresh-spawn path.
func TestCmdDoTabTitleFormat(t *testing.T) {
	setupFlowRoot(t)
	seedProject(t, "tekion-modules")
	for i, tc := range []struct{ env, want string }{
		{"", "tekion-modules/tk-%d"},
		{"project/task", "tekion-modules/tk-%d"},
		{"task", "tk-%d"},
		{" Task ", "tk-%d"},
		{"bogus", "tekion-modules/tk-%d"},
	} {
		slug := fmt.Sprintf("tk-%d", i)
		if rc := cmdAdd([]string{"task", slug, "--project", "tekion-modules"}); rc != 0 {
			t.Fatalf("add task rc=%d", rc)
		}
		t.Setenv("FLOW_TAB_TITLE", tc.env)
		calls := stubRex(t)
		if rc := cmdDo([]string{slug}); rc != 0 {
			t.Fatalf("do rc=%d", rc)
		}
		got := "<no window new>"
		for _, c := range *calls {
			if len(c) > 2 && c[0] == "window" && c[1] == "new" {
				got = c[2]
				break
			}
		}
		if want := fmt.Sprintf(tc.want, i); got != want {
			t.Errorf("FLOW_TAB_TITLE=%q: tab title = %q; want %q", tc.env, got, want)
		}
	}
}

// TestBuildTabTitle — formats, floating tasks, and truncation that
// shortens the project part first so the task stays visible.
func TestBuildTabTitle(t *testing.T) {
	task := &flowdb.Task{Slug: "tk-task"}
	proj := &flowdb.Project{Slug: "tekion-modules"}
	longProj := &flowdb.Project{Slug: "tenant-level-aws-resources-xyz"}
	longTask := &flowdb.Task{Slug: "tenant-level-aws-resources-blueprint-import"}
	for _, tc := range []struct {
		name   string
		p      *flowdb.Project
		t      *flowdb.Task
		format tabTitleFormat
		want   string
	}{
		{"project/task", proj, task, titleProjectTask, "tekion-modules/tk-task"},
		{"task format", proj, task, titleTask, "tk-task"},
		{"floating", nil, task, titleProjectTask, "tk-task"},
		{"long project keeps task", longProj, &flowdb.Task{Slug: "fix-db"}, titleProjectTask, "tenant-level-aws-resou…/fix-db"},
		{"long task drops project", proj, longTask, titleProjectTask, "tenant-level-aws-resources-bl…"},
		{"long task, task format", proj, longTask, titleTask, "tenant-level-aws-resources-bl…"},
	} {
		got := buildTabTitle(tc.p, tc.t, tc.format)
		if got != tc.want {
			t.Errorf("%s: got %q; want %q", tc.name, got, tc.want)
		}
		if n := len([]rune(got)); n > tabTitleMaxLen {
			t.Errorf("%s: %d runes > %d", tc.name, n, tabTitleMaxLen)
		}
	}
}
