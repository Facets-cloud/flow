package app

import (
	"encoding/json"
	"flow/internal/flowdb"
	"flow/internal/harness/claude"
	"os"
	"path/filepath"
	"testing"
)

// `flow transcript <slug> --json` emits one harness-neutral JSON document:
// the task, its harness and session, and normalised entries.
func TestTranscriptJSON(t *testing.T) {
	root := setupFlowRoot(t)
	_ = root
	home := os.Getenv("HOME")
	repo := filepath.Join(home, "code", "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if rc := cmdAdd([]string{"task", "TX JSON", "--slug", "tx-json", "--work-dir", repo}); rc != 0 {
		t.Fatalf("add rc=%d", rc)
	}
	db := openFlowDB(t)
	const sid = "deadbeef-1234-5678-9abc-def012345678"
	if _, err := db.Exec(`UPDATE tasks SET status='in-progress', session_id=?, session_started=?, harness='claude' WHERE slug='tx-json'`,
		sid, flowdb.NowISO()); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".claude", "projects", claude.EncodeCwd(repo))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonl := `{"type":"user","message":{"role":"user","content":"hi there"},"timestamp":"2026-04-12T10:00:00Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"hello"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]},"timestamp":"2026-04-12T10:00:01Z"}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a\nb"}]},"timestamp":"2026-04-12T10:00:02Z"}
`
	if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}

	var rc int
	out := captureStdout(t, func() { rc = cmdTranscript([]string{"tx-json", "--json"}) })
	if rc != 0 {
		t.Fatalf("rc=%d out=%s", rc, out)
	}
	var doc struct {
		Schema    int    `json:"schema"`
		Task      string `json:"task"`
		Harness   string `json:"harness"`
		SessionID string `json:"session_id"`
		Entries   []struct {
			TS, Role, Kind, Text, Tool string
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if doc.Schema != 1 || doc.Task != "tx-json" || doc.Harness != "claude" || doc.SessionID != sid {
		t.Errorf("header = %+v", doc)
	}
	if len(doc.Entries) != 5 {
		t.Fatalf("entries = %+v", doc.Entries)
	}
	if e := doc.Entries[0]; e.Role != "user" || e.Kind != "text" || e.Text != "hi there" || e.TS != "2026-04-12T10:00:00Z" {
		t.Errorf("entry0 = %+v", e)
	}
	if e := doc.Entries[3]; e.Kind != "tool_call" || e.Tool != "Bash" || e.Text != "$ ls" {
		t.Errorf("entry3 = %+v", e)
	}
	if e := doc.Entries[4]; e.Kind != "tool_result" || e.Tool != "Bash" || e.Text != "a\nb" {
		t.Errorf("entry4 = %+v", e)
	}

	// --compact drops thinking and tool results.
	out = captureStdout(t, func() { rc = cmdTranscript([]string{"tx-json", "--json", "--compact"}) })
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc.Entries) != 3 {
		t.Errorf("compact entries = %+v err=%v", doc.Entries, err)
	}
}
