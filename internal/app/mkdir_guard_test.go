package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

// --mkdir alone means "give this task its own fresh dir". It used to be
// silently ignored, so a task meant for a fresh dir landed in the
// (inherited) project's repo instead.
func TestCmdAddTaskMkdirAloneGivesFreshWorkspace(t *testing.T) {
	root := setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"project", "P", "--work-dir", wd}); rc != 0 {
		t.Fatalf("add project rc=%d", rc)
	}
	out := captureStdout(t, func() {
		if rc := cmdAdd([]string{"task", "Scratch", "--project", "p", "--mkdir"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	want := filepath.Join(root, "tasks", "scratch", "workspace")
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "scratch")
	if err != nil {
		t.Fatal(err)
	}
	if task.WorkDir != want {
		t.Errorf("work_dir = %q, want fresh workspace %q", task.WorkDir, want)
	}
	if !task.ProjectSlug.Valid || task.ProjectSlug.String != "p" {
		t.Errorf("project should still be set: %+v", task.ProjectSlug)
	}
	if st, err := os.Stat(want); err != nil || !st.IsDir() {
		t.Errorf("workspace dir not created: %v", err)
	}
	if !strings.Contains(out, "(new workspace)") {
		t.Errorf("output should name the source:\n%s", out)
	}
}

func TestCmdAddTaskPrintsWorkDirSource(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"project", "P", "--work-dir", wd}); rc != 0 {
		t.Fatalf("add project rc=%d", rc)
	}
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"task", "From Project", "--project", "p"}, "work_dir: " + wd + " (from project \"p\")"},
		{[]string{"task", "Floating"}, "(new workspace)"},
		{[]string{"task", "Explicit", "--work-dir", wd}, "work_dir: " + wd + "\n"},
	}
	for _, c := range cases {
		out := captureStdout(t, func() {
			if rc := cmdAdd(c.args); rc != 0 {
				t.Fatalf("%v rc=%d", c.args, rc)
			}
		})
		if !strings.Contains(out, c.want) {
			t.Errorf("%v: output missing %q:\n%s", c.args, c.want, out)
		}
	}
}

func TestCmdUpdateTaskMkdirWithoutWorkDirErrors(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "T"}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	stderr := captureStderr(t)
	if rc := cmdUpdate([]string{"task", "t", "--mkdir", "--priority", "high"}); rc != 2 {
		t.Fatalf("rc=%d, want 2", rc)
	}
	if msg := stderr(); !strings.Contains(msg, "--mkdir requires --work-dir") {
		t.Errorf("stderr: %q", msg)
	}
}
