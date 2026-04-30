package app

import (
	"testing"

	"flow/internal/flowdb"
)

func TestResolvePlaybook(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	wd := t.TempDir()
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "triage-cs", Name: "Triage", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}

	pb, err := ResolvePlaybook(db, "triage-cs", false)
	if err != nil {
		t.Fatalf("ResolvePlaybook: %v", err)
	}
	if pb.Slug != "triage-cs" {
		t.Errorf("got slug %q", pb.Slug)
	}

	if _, err := ResolvePlaybook(db, "no-such", false); err == nil {
		t.Errorf("expected error for missing slug")
	}
}

func TestResolvePlaybookArchived(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	wd := t.TempDir()
	if err := flowdb.UpsertPlaybook(db, &flowdb.Playbook{Slug: "old", Name: "O", WorkDir: wd}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE playbooks SET archived_at = ? WHERE slug = ?`, flowdb.NowISO(), "old"); err != nil {
		t.Fatal(err)
	}

	if _, err := ResolvePlaybook(db, "old", false); err == nil {
		t.Errorf("expected error for archived playbook (includeArchived=false)")
	}
	if _, err := ResolvePlaybook(db, "old", true); err != nil {
		t.Errorf("expected to find archived playbook with includeArchived=true: %v", err)
	}
}
