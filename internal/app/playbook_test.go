package app

import (
	"os"
	"path/filepath"
	"testing"

	"flow/internal/flowdb"
)

func TestCmdAddPlaybookHappyPath(t *testing.T) {
	root := setupFlowRoot(t)
	wd := t.TempDir()

	rc := cmdAdd([]string{"playbook", "Triage CS inbox", "--work-dir", wd, "--slug", "triage-cs"})
	if rc != 0 {
		t.Fatalf("cmdAdd rc=%d", rc)
	}

	db := openFlowDB(t)
	pb, err := flowdb.GetPlaybook(db, "triage-cs")
	if err != nil {
		t.Fatalf("GetPlaybook: %v", err)
	}
	if pb.Name != "Triage CS inbox" {
		t.Errorf("name: got %q", pb.Name)
	}
	if pb.WorkDir != wd {
		t.Errorf("work_dir: got %q", pb.WorkDir)
	}

	briefPath := filepath.Join(root, "playbooks", "triage-cs", "brief.md")
	if _, err := os.Stat(briefPath); err != nil {
		t.Errorf("brief.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "playbooks", "triage-cs", "updates")); err != nil {
		t.Errorf("updates/ dir missing: %v", err)
	}
}

func TestCmdAddPlaybookRequiresWorkDir(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"playbook", "NoWD"}); rc == 0 {
		t.Errorf("expected non-zero rc when --work-dir missing")
	}
}

func TestCmdAddPlaybookWithProject(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"project", "CS Tools", "--slug", "cs-tools", "--work-dir", wd}); rc != 0 {
		t.Fatal()
	}

	if rc := cmdAdd([]string{"playbook", "Triage", "--slug", "tri", "--work-dir", wd, "--project", "cs-tools"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db := openFlowDB(t)
	pb, err := flowdb.GetPlaybook(db, "tri")
	if err != nil {
		t.Fatal(err)
	}
	if !pb.ProjectSlug.Valid || pb.ProjectSlug.String != "cs-tools" {
		t.Errorf("project_slug: got %+v", pb.ProjectSlug)
	}
}

func TestCmdAddPlaybookProjectNotFound(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"playbook", "X", "--slug", "x", "--work-dir", wd, "--project", "nope"}); rc == 0 {
		t.Errorf("expected non-zero rc when project doesn't exist")
	}
}
