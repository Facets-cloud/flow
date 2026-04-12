package app

import (
	"flow/internal/flowdb"
	"flow/internal/iterm"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sampleSessionJSONL is a minimal but representative session jsonl for
// testing the transcript parser. It includes each record type the parser
// must handle: permission-mode (skip), user text, assistant thinking,
// assistant text, assistant tool_use, user tool_result, and
// file-history-snapshot (skip).
const sampleSessionJSONL = `{"type":"permission-mode","permissionMode":"default","sessionId":"test-sid"}
{"type":"file-history-snapshot","messageId":"m1","snapshot":{},"isSnapshotUpdate":false}
{"parentUuid":null,"isSidechain":false,"type":"user","message":{"role":"user","content":"Hello, can you help me?"},"uuid":"u1","timestamp":"2026-04-12T10:00:00.000Z","sessionId":"test-sid"}
{"parentUuid":"u1","isSidechain":false,"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"The user wants help."},{"type":"text","text":"Sure, let me check."}]},"uuid":"a1","timestamp":"2026-04-12T10:00:01.000Z","sessionId":"test-sid"}
{"parentUuid":"a1","isSidechain":false,"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"echo hello"}}]},"uuid":"a2","timestamp":"2026-04-12T10:00:02.000Z","sessionId":"test-sid"}
{"parentUuid":"a2","isSidechain":false,"type":"user","message":{"role":"user","content":[{"tool_use_id":"tu1","type":"tool_result","content":"hello\n","is_error":false}]},"uuid":"u2","timestamp":"2026-04-12T10:00:03.000Z","sessionId":"test-sid"}
{"parentUuid":"u2","isSidechain":false,"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu2","name":"Read","input":{"file_path":"/tmp/test.go","offset":10,"limit":20}}]},"uuid":"a3","timestamp":"2026-04-12T10:00:04.000Z","sessionId":"test-sid"}
{"parentUuid":"a3","isSidechain":false,"type":"user","message":{"role":"user","content":[{"tool_use_id":"tu2","type":"tool_result","content":"package main\n","is_error":false}]},"uuid":"u3","timestamp":"2026-04-12T10:00:05.000Z","sessionId":"test-sid"}
{"parentUuid":"u3","isSidechain":false,"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done!"}]},"uuid":"a4","timestamp":"2026-04-12T10:00:06.000Z","sessionId":"test-sid"}
`

func TestTranscriptRender(t *testing.T) {
	tmp := t.TempDir()
	jsonlPath := filepath.Join(tmp, "test-sid.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(sampleSessionJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	rc := renderTranscript(jsonlPath, false)

	w.Close()
	os.Stdout = old

	if rc != 0 {
		t.Fatalf("renderTranscript returned %d, want 0", rc)
	}

	buf := make([]byte, 64*1024)
	n, _ := r.Read(buf)
	out := string(buf[:n])

	// Verify key sections appear.
	checks := []struct {
		label    string
		contains string
	}{
		{"user message", "─── User ───"},
		{"user text", "Hello, can you help me?"},
		{"thinking", "─── Thinking ───"},
		{"thinking text", "The user wants help."},
		{"assistant text", "Sure, let me check."},
		{"tool use bash", "─── Tool: Bash ───"},
		{"bash command", "$ echo hello"},
		{"tool result", "─── Result ───"},
		{"result content", "hello"},
		{"tool use read", "─── Tool: Read ───"},
		{"read path", "/tmp/test.go"},
		{"read offset", "offset=10"},
		{"read limit", "limit=20"},
		{"final text", "Done!"},
	}
	for _, c := range checks {
		if !strings.Contains(out, c.contains) {
			t.Errorf("%s: output missing %q", c.label, c.contains)
		}
	}

	// Verify skipped types don't leak.
	if strings.Contains(out, "permission-mode") {
		t.Error("permission-mode record leaked into output")
	}
	if strings.Contains(out, "file-history-snapshot") {
		t.Error("file-history-snapshot record leaked into output")
	}
}

func TestTranscriptCompact(t *testing.T) {
	tmp := t.TempDir()
	jsonlPath := filepath.Join(tmp, "test-sid.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(sampleSessionJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	rc := renderTranscript(jsonlPath, true)

	w.Close()
	os.Stdout = old

	if rc != 0 {
		t.Fatalf("renderTranscript returned %d, want 0", rc)
	}

	buf := make([]byte, 64*1024)
	n, _ := r.Read(buf)
	out := string(buf[:n])

	// Compact should still have user messages and tool calls.
	if !strings.Contains(out, "Hello, can you help me?") {
		t.Error("compact missing user message")
	}
	if !strings.Contains(out, "─── Tool: Bash ───") {
		t.Error("compact missing tool use")
	}
	if !strings.Contains(out, "Done!") {
		t.Error("compact missing assistant text")
	}

	// Compact should omit thinking and tool results.
	if strings.Contains(out, "─── Thinking ───") {
		t.Error("compact should omit thinking blocks")
	}
	if strings.Contains(out, "─── Result") {
		t.Error("compact should omit tool results")
	}
}

func TestTranscriptCmdNoSession(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("FLOW_ROOT", filepath.Join(tmp, "flow"))
	t.Setenv("HOME", tmp)

	oldOsa := iterm.Runner
	iterm.Runner = func(args []string) error { return nil }
	t.Cleanup(func() { iterm.Runner = oldOsa })

	cmdInit(nil)
	cmdAdd([]string{"task", "No Session Task", "--slug", "no-session"})

	rc := cmdTranscript([]string{"no-session"})
	if rc != 1 {
		t.Errorf("transcript with no session: rc=%d, want 1", rc)
	}
}

func TestTranscriptCmdWithSession(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("FLOW_ROOT", filepath.Join(tmp, "flow"))
	t.Setenv("HOME", tmp)

	oldOsa := iterm.Runner
	iterm.Runner = func(args []string) error { return nil }
	t.Cleanup(func() { iterm.Runner = oldOsa })

	cmdInit(nil)

	repo := filepath.Join(tmp, "code", "myrepo")
	os.MkdirAll(repo, 0o755)
	cmdAdd([]string{"task", "Transcript Test", "--slug", "tx-test", "--work-dir", repo})

	// Simulate session bootstrap.
	dbPath, _ := flowDBPath()
	db, _ := flowdb.OpenDB(dbPath)
	defer db.Close()

	sid := "deadbeef-1234-5678-9abc-def012345678"
	now := flowdb.NowISO()
	db.Exec(`UPDATE tasks SET session_id=?, session_started=?, updated_at=? WHERE slug=?`,
		sid, now, now, "tx-test")

	// Write a minimal jsonl.
	encoded := EncodeCwdForClaude(repo)
	sessionDir := filepath.Join(tmp, ".claude", "projects", encoded)
	os.MkdirAll(sessionDir, 0o755)
	os.WriteFile(
		filepath.Join(sessionDir, sid+".jsonl"),
		[]byte(`{"type":"user","message":{"role":"user","content":"test message"},"uuid":"u1","timestamp":"2026-04-12T10:00:00Z","sessionId":"`+sid+`"}`+"\n"),
		0o644,
	)

	rc := cmdTranscript([]string{"tx-test"})
	if rc != 0 {
		t.Errorf("transcript with session: rc=%d, want 0", rc)
	}
}
