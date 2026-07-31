package app

import (
	"os"
	"path/filepath"
	"testing"

	"flow/internal/flowdb"
)

// TestCmdDoHereBindsPraxisSession verifies --here recognizes the session id
// exported by Praxis, validates its native nested transcript location, and
// records the Praxis harness pin. This is the in-session counterpart to
// `flow do <task> --harness praxis`.
func TestCmdDoHereBindsPraxisSession(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "praxis-here")

	const sid = "658bf2be-5ae3-4842-a8a4-e0d0b785514d"
	transcript := filepath.Join(os.Getenv("HOME"), ".praxis", "agent", "sessions", sid, "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte(`{"type":"session","version":3}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("PRAXIS_SESSION_ID", sid)

	if rc := cmdDo([]string{"praxis-here", "--here"}); rc != 0 {
		t.Fatalf("cmdDo --here rc=%d", rc)
	}

	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "praxis-here")
	if err != nil {
		t.Fatal(err)
	}
	if !task.SessionID.Valid || task.SessionID.String != sid {
		t.Errorf("session_id = %+v, want %s", task.SessionID, sid)
	}
	if !task.Harness.Valid || task.Harness.String != "praxis" {
		t.Errorf("harness = %+v, want praxis", task.Harness)
	}
	if task.Status != "in-progress" {
		t.Errorf("status = %q, want in-progress", task.Status)
	}
}

// --here binds the session the user is already in, so its harness is
// fixed by the ambient env. Accepting --harness alongside it would look
// like it selected something while being silently ignored.
func TestCmdDoHereRejectsHarnessFlag(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "praxis-here")

	const sid = "658bf2be-5ae3-4842-a8a4-e0d0b785514d"
	t.Setenv("PRAXIS_SESSION_ID", sid)

	for _, name := range []string{"praxis", "claude"} {
		if rc := cmdDo([]string{"praxis-here", "--here", "--harness", name}); rc != 2 {
			t.Errorf("cmdDo --here --harness %s rc=%d, want 2", name, rc)
		}
	}

	// And nothing was bound.
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "praxis-here")
	if err != nil {
		t.Fatal(err)
	}
	if task.SessionID.Valid && task.SessionID.String != "" {
		t.Errorf("session_id = %q, want unset after a usage error", task.SessionID.String)
	}
}
