package app

import (
	"database/sql"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

// Human connectors MVP — one block per numbered behavior of the spec.

// msgRC runs `flow message <args...>` swallowing stdout and returns rc.
func msgRC(t *testing.T, args ...string) int {
	t.Helper()
	var rc int
	captureStdout(t, func() { rc = cmdMessage(args) })
	return rc
}

// rowByBody fetches the single bus row with this body.
func rowByBody(t *testing.T, db *sql.DB, body string) *flowdb.BusMessage {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT id FROM bus_messages WHERE body=?`, body).Scan(&id); err != nil {
		t.Fatalf("row %q: %v", body, err)
	}
	m, err := flowdb.GetBusMessageByID(db, id)
	if err != nil || m == nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return m
}

// B1: --options and --fyi on human-addressed mail only.
func TestB1MessageOptionsAndFYI(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "asker", "sid-asker")
	mkBusTask(t, db, "peer", "sid-peer")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-asker")

	if rc := msgRC(t, "user", "ship it?", "--options", "Yes|No|Later today"); rc != 0 {
		t.Fatalf("options rc=%d", rc)
	}
	m := rowByBody(t, db, "ship it?")
	if got := m.Options; len(got) != 3 || got[0] != "Yes" || got[1] != "No" || got[2] != "Later today" {
		t.Errorf("options = %q", got)
	}
	if m.FYI {
		t.Errorf("plain ask stored as fyi")
	}

	if rc := msgRC(t, "user", "--fyi", "progress: 3/5 done"); rc != 0 {
		t.Fatalf("fyi rc=%d", rc)
	}
	if f := rowByBody(t, db, "progress: 3/5 done"); !f.FYI || len(f.Options) != 0 {
		t.Errorf("fyi row: fyi=%v options=%q", f.FYI, f.Options)
	}

	asks, fyi, err := flowdb.HumanUnreadCounts(db, "user")
	if err != nil {
		t.Fatal(err)
	}
	if asks != 1 || fyi != 1 {
		t.Errorf("unread counts asks=%d fyi=%d, want 1/1 (an --fyi message is FYI)", asks, fyi)
	}

	// Listed: the human's inbox shows the choices and the FYI marker, and
	// the JSON rendering carries both.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	out := captureStdout(t, func() { _ = cmdInbox(nil) })
	if !strings.Contains(out, "(Yes | No | Later today)") || !strings.Contains(out, "fyi") {
		t.Errorf("inbox listing lacks options/fyi:\n%s", out)
	}
	out = captureStdout(t, func() { _ = cmdInbox([]string{"--json"}) })
	if !strings.Contains(out, `"options":["Yes","No","Later today"]`) || !strings.Contains(out, `"fyi":true`) {
		t.Errorf("inbox json lacks options/fyi:\n%s", out)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-asker")

	for name, args := range map[string][]string{
		"options to task":  {"user/peer", "hi", "--options", "A|B"},
		"fyi to task":      {"user/peer", "hi", "--fyi"},
		"fyi + urgent":     {"user", "x1", "--fyi", "--urgent"},
		"fyi + options":    {"user", "x2", "--fyi", "--options", "A|B"},
		"empty options":    {"user", "x3", "--options", " | "},
		"options no value": {"user", "x4", "--options"},
	} {
		if rc := msgRC(t, args...); rc != 2 {
			t.Errorf("%s: rc=%d, want 2", name, rc)
		}
	}
}
