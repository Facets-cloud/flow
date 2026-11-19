package app

import (
	"fmt"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

// A flow.db migrated by a newer, incompatible flow: regular commands fail
// with the "run flow upgrade" error, hooks swallow it and see the session
// as unbound (a hook error would block the user's session).
func TestSchemaTooNewHooksUnboundCommandsError(t *testing.T) {
	setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"task", "Compat", "--work-dir", wd}); rc != 0 {
		t.Fatalf("add task rc=%d", rc)
	}
	bindSession(t, "compat", "sid-compat")
	if lookupBoundTask() == nil {
		t.Fatal("precondition: session should be bound")
	}

	db := openFlowDB(t)
	future := flowdb.LatestSchemaVersion() + 5
	if _, err := db.Exec(`UPDATE schema_meta SET min_binary_schema = ?`, future); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, future)); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if lookupBoundTask() != nil {
		t.Error("too-new DB: hooks must see the session as unbound")
	}
	payload := `{"session_id":"sid-compat","prompt":"hi"}`
	for name, fn := range map[string]func([]string) int{
		"session-start":      cmdHookSessionStart,
		"user-prompt-submit": cmdHookUserPromptSubmit,
		"pre-tool-use":       cmdHookPreToolUse,
		"stop":               cmdHookStop,
	} {
		rc := -1
		withHookStdin(t, payload, func() {
			_ = captureStdout(t, func() { rc = fn(nil) })
		})
		hookSessionID, hookPrompt, hookPromptKnown = "", "", false
		if rc != 0 {
			t.Errorf("hook %s rc=%d, want 0", name, rc)
		}
	}

	errOut := captureStderr(t)
	rc := cmdList([]string{"tasks"})
	stderr := errOut()
	if rc != 1 {
		t.Errorf("list tasks rc=%d, want 1", rc)
	}
	if !strings.Contains(stderr, "flow upgrade") {
		t.Errorf("stderr should tell the user to run flow upgrade: %q", stderr)
	}
}
