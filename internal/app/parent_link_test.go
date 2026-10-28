package app

import (
	"strings"
	"testing"

	"flow/internal/flowdb"
)

func TestAddTaskRecordsBoundTaskAsParent(t *testing.T) {
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
	task, err := flowdb.GetTask(openFlowDB(t), "child-audit")
	if err != nil {
		t.Fatal(err)
	}
	if !task.ParentSlug.Valid || task.ParentSlug.String != "modelling" {
		t.Errorf("parent_slug: got %+v, want modelling", task.ParentSlug)
	}
	if !strings.Contains(out, `(parent: "modelling")`) {
		t.Errorf("add output should mention parent, got:\n%s", out)
	}
}

func TestAddTaskUnboundHasNoParent(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "")
	var rc int
	out := captureStdout(t, func() { rc = cmdAdd([]string{"task", "Solo"}) })
	if rc != 0 {
		t.Fatal("add task")
	}
	task, _ := flowdb.GetTask(openFlowDB(t), "solo")
	if task.ParentSlug.Valid {
		t.Errorf("expected NULL parent_slug, got %q", task.ParentSlug.String)
	}
	if strings.Contains(out, "parent:") {
		t.Errorf("unbound add should not mention parent, got:\n%s", out)
	}
}

func TestShowTaskPrintsParentAndChildren(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "Root"}); rc != 0 {
		t.Fatal("add root")
	}
	bindSession(t, "root", "sid-root")
	for _, c := range []string{"Kid B", "Kid A"} {
		if rc := cmdAdd([]string{"task", c}); rc != 0 {
			t.Fatalf("add %s", c)
		}
	}

	out := captureStdout(t, func() { cmdShow([]string{"task", "root"}) })
	if !strings.Contains(out, "spun_off:      kid-a, kid-b") {
		t.Errorf("show root should list children, got:\n%s", out)
	}
	if strings.Contains(out, "parent:") {
		t.Errorf("root has no parent, got:\n%s", out)
	}

	out = captureStdout(t, func() { cmdShow([]string{"task", "kid-a"}) })
	if !strings.Contains(out, "parent:        root") {
		t.Errorf("show kid-a should print parent, got:\n%s", out)
	}
	if strings.Contains(out, "spun_off:") {
		t.Errorf("kid-a has no children, got:\n%s", out)
	}
}
