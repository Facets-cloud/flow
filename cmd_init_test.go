package main

import (
	"os"
	"path/filepath"
	"testing"
)

// initTempFlowRoot points FLOW_ROOT at a tempdir AND redirects $HOME so
// skill install lands inside the tempdir as well. Isolates every init test
// from the real ~/.flow/ and ~/.claude/skills/. Named with an `init` prefix
// to avoid colliding with withTempFlowRoot defined in cmd_show_test.go
// (which has a different signature because it returns (root, db)).
func initTempFlowRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	home := t.TempDir()

	oldRoot := os.Getenv("FLOW_ROOT")
	oldHome := os.Getenv("HOME")
	os.Setenv("FLOW_ROOT", root)
	os.Setenv("HOME", home)
	t.Cleanup(func() {
		os.Setenv("FLOW_ROOT", oldRoot)
		os.Setenv("HOME", oldHome)
	})
	return root
}

func TestCmdInitCreatesTree(t *testing.T) {
	root := initTempFlowRoot(t)

	if rc := cmdInit(nil); rc != 0 {
		t.Fatalf("cmdInit rc=%d", rc)
	}
	for _, sub := range []string{"projects", "tasks", "bin"} {
		p := filepath.Join(root, sub)
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("missing dir %s: %v", p, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", p)
		}
	}
}

func TestCmdInitCreatesDBQueryable(t *testing.T) {
	root := initTempFlowRoot(t)
	if rc := cmdInit(nil); rc != 0 {
		t.Fatalf("cmdInit rc=%d", rc)
	}
	dbPath := filepath.Join(root, "flow.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("flow.db missing: %v", err)
	}
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	// Confirm a core table exists.
	var name string
	if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='tasks'").Scan(&name); err != nil {
		t.Errorf("tasks table missing: %v", err)
	}
}

func TestCmdInitIdempotent(t *testing.T) {
	root := initTempFlowRoot(t)
	if rc := cmdInit(nil); rc != 0 {
		t.Fatalf("first init rc=%d", rc)
	}
	if rc := cmdInit(nil); rc != 0 {
		t.Fatalf("second init rc=%d", rc)
	}
	// Tree and DB still there.
	if _, err := os.Stat(filepath.Join(root, "flow.db")); err != nil {
		t.Errorf("flow.db missing after second init: %v", err)
	}
}

func TestCmdInitInstallsSkill(t *testing.T) {
	initTempFlowRoot(t)
	if rc := cmdInit(nil); rc != 0 {
		t.Fatalf("cmdInit rc=%d", rc)
	}
	skillPath := filepath.Join(os.Getenv("HOME"), ".claude", "skills", "flow", "SKILL.md")
	info, err := os.Stat(skillPath)
	if err != nil {
		t.Fatalf("SKILL.md missing: %v", err)
	}
	if info.Size() == 0 {
		t.Errorf("SKILL.md is empty")
	}
}

func TestCmdInitSkipsSkillIfAlreadyPresent(t *testing.T) {
	initTempFlowRoot(t)
	home := os.Getenv("HOME")
	skillPath := filepath.Join(home, ".claude", "skills", "flow", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("existing content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rc := cmdInit(nil); rc != 0 {
		t.Fatalf("cmdInit rc=%d", rc)
	}
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing content" {
		t.Errorf("init overwrote existing skill: %q", string(data))
	}
}

func TestCmdInitRejectsExtraArgs(t *testing.T) {
	initTempFlowRoot(t)
	if rc := cmdInit([]string{"extra"}); rc != 2 {
		t.Errorf("expected rc=2 for extra args, got %d", rc)
	}
}
