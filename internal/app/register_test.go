package app

import (
	"flow/internal/flowdb"
	"os"
	"path/filepath"
	"testing"
)

// writeSessionFile simulates claude creating its jsonl for a session.
// The register-session command finds the newest such file in the
// encoded work_dir and writes its UUID back to the DB.
func writeSessionFile(t *testing.T, home, workDir, sid string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", EncodeCwdForClaude(workDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRegisterSessionFromEnv(t *testing.T) {
	root := setupFlowRoot(t)
	tmp := filepath.Dir(root)
	t.Setenv("HOME", tmp)

	seedTask(t, "register-me")
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "register-me")
	db.Close()

	writeSessionFile(t, tmp, task.WorkDir, "captured-sid-1")
	t.Setenv("FLOW_TASK", "register-me")

	if rc := cmdRegisterSession(nil); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}

	db = openFlowDB(t)
	task, _ = flowdb.GetTask(db, "register-me")
	if !task.SessionID.Valid || task.SessionID.String != "captured-sid-1" {
		t.Errorf("session_id = %+v, want captured-sid-1", task.SessionID)
	}
}

func TestRegisterSessionExplicitSlug(t *testing.T) {
	root := setupFlowRoot(t)
	tmp := filepath.Dir(root)
	t.Setenv("HOME", tmp)

	seedTask(t, "explicit")
	db := openFlowDB(t)
	task, _ := flowdb.GetTask(db, "explicit")
	db.Close()

	writeSessionFile(t, tmp, task.WorkDir, "sid-explicit")

	if rc := cmdRegisterSession([]string{"explicit"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db = openFlowDB(t)
	task, _ = flowdb.GetTask(db, "explicit")
	if task.SessionID.String != "sid-explicit" {
		t.Errorf("session_id = %q, want sid-explicit", task.SessionID.String)
	}
}

func TestRegisterSessionRefusesOverwrite(t *testing.T) {
	root := setupFlowRoot(t)
	tmp := filepath.Dir(root)
	t.Setenv("HOME", tmp)

	seedTask(t, "already-set")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET session_id='already-here' WHERE slug='already-set'`); err != nil {
		t.Fatal(err)
	}
	task, _ := flowdb.GetTask(db, "already-set")
	db.Close()

	writeSessionFile(t, tmp, task.WorkDir, "would-overwrite")

	// No --force → should preserve existing value, rc=0 (non-fatal).
	if rc := cmdRegisterSession([]string{"already-set"}); rc != 0 {
		t.Errorf("rc=%d (non-fatal refuse)", rc)
	}
	db = openFlowDB(t)
	task, _ = flowdb.GetTask(db, "already-set")
	if task.SessionID.String != "already-here" {
		t.Errorf("existing session_id was overwritten: got %q, want already-here", task.SessionID.String)
	}
}

func TestRegisterSessionForceOverwrites(t *testing.T) {
	root := setupFlowRoot(t)
	tmp := filepath.Dir(root)
	t.Setenv("HOME", tmp)

	seedTask(t, "force-me")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET session_id='old' WHERE slug='force-me'`); err != nil {
		t.Fatal(err)
	}
	task, _ := flowdb.GetTask(db, "force-me")
	db.Close()

	writeSessionFile(t, tmp, task.WorkDir, "brand-new")

	if rc := cmdRegisterSession([]string{"--force", "force-me"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	db = openFlowDB(t)
	task, _ = flowdb.GetTask(db, "force-me")
	if task.SessionID.String != "brand-new" {
		t.Errorf("--force didn't overwrite: got %q, want brand-new", task.SessionID.String)
	}
}

func TestRegisterSessionNoSessionFile(t *testing.T) {
	root := setupFlowRoot(t)
	tmp := filepath.Dir(root)
	t.Setenv("HOME", tmp)

	seedTask(t, "no-session")
	// Do NOT write any jsonl file.

	if rc := cmdRegisterSession([]string{"no-session"}); rc != 1 {
		t.Errorf("rc=%d, want 1 when no jsonl present", rc)
	}
}

func TestRegisterSessionNoEnvAndNoArg(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_TASK", "")

	if rc := cmdRegisterSession(nil); rc != 2 {
		t.Errorf("rc=%d, want 2 when no slug and no env", rc)
	}
}
