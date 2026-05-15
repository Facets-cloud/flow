package app

import (
	"flow/internal/flowdb"
	"flow/internal/iterm"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

const sampleCodexSessionJSONL = `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Codex hello"}]}}
{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Codex reply"}]}}
{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"pwd\"}"}}
{"type":"response_item","payload":{"type":"function_call_output","output":"repo\n"}}
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

	rc := renderTranscript(jsonlPath, false, time.Time{})

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

	rc := renderTranscript(jsonlPath, true, time.Time{})

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

// ---------- timestamp cutoff filter ----------

// preBindJSONL has two user messages: one before session_started (pre-bind
// chatter from the dispatch session) and one after. The filter should drop
// the pre-bind entry but keep the post-bind one.
const preBindJSONL = `{"type":"user","message":{"role":"user","content":"PRE-BIND chatter"},"uuid":"u0","timestamp":"2026-05-14T10:00:00.000Z","sessionId":"sid"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"PRE-BIND reply"}]},"uuid":"a0","timestamp":"2026-05-14T10:00:01.000Z","sessionId":"sid"}
{"type":"user","message":{"role":"user","content":"POST-BIND request"},"uuid":"u1","timestamp":"2026-05-14T11:00:00.000Z","sessionId":"sid"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"POST-BIND reply"}]},"uuid":"a1","timestamp":"2026-05-14T11:00:01.000Z","sessionId":"sid"}
`

// TestTranscriptFiltersByCutoff pins the --here close-out sweep contract:
// entries with timestamp < cutoff are dropped. Entries at-or-after are kept.
func TestTranscriptFiltersByCutoff(t *testing.T) {
	tmp := t.TempDir()
	jsonlPath := filepath.Join(tmp, "sid.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(preBindJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	cutoff, _ := time.Parse(time.RFC3339Nano, "2026-05-14T10:30:00.000Z")

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	rc := renderTranscript(jsonlPath, false, cutoff)
	w.Close()
	os.Stdout = old
	if rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
	buf := make([]byte, 64*1024)
	n, _ := r.Read(buf)
	out := string(buf[:n])

	if strings.Contains(out, "PRE-BIND") {
		t.Errorf("pre-bind content leaked through filter:\n%s", out)
	}
	if !strings.Contains(out, "POST-BIND request") {
		t.Errorf("post-bind user message dropped:\n%s", out)
	}
	if !strings.Contains(out, "POST-BIND reply") {
		t.Errorf("post-bind assistant message dropped:\n%s", out)
	}
}

// TestTranscriptZeroCutoffNoFilter pins the no-op case: when cutoff is
// the zero time.Time, the filter is bypassed and all entries pass.
// This is what cmdTranscript should hand to renderTranscript when
// task.session_started is NULL/empty.
func TestTranscriptZeroCutoffNoFilter(t *testing.T) {
	tmp := t.TempDir()
	jsonlPath := filepath.Join(tmp, "sid.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(preBindJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	rc := renderTranscript(jsonlPath, false, time.Time{})
	w.Close()
	os.Stdout = old
	if rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
	buf := make([]byte, 64*1024)
	n, _ := r.Read(buf)
	out := string(buf[:n])

	if !strings.Contains(out, "PRE-BIND chatter") {
		t.Errorf("zero cutoff should pass everything; pre-bind missing:\n%s", out)
	}
	if !strings.Contains(out, "POST-BIND request") {
		t.Errorf("zero cutoff should pass everything; post-bind missing:\n%s", out)
	}
}

// TestTranscriptKeepsEntriesWithMissingTimestamp pins the defensive
// behavior: an entry without a `timestamp` field is KEPT regardless of
// cutoff. The user explicitly asked: don't silently lose data.
func TestTranscriptKeepsEntriesWithMissingTimestamp(t *testing.T) {
	tmp := t.TempDir()
	jsonlPath := filepath.Join(tmp, "sid.jsonl")
	const j = `{"type":"user","message":{"role":"user","content":"NO TIMESTAMP entry"},"uuid":"u0","sessionId":"sid"}
{"type":"user","message":{"role":"user","content":"BEFORE cutoff"},"uuid":"u1","timestamp":"2026-05-14T09:00:00.000Z","sessionId":"sid"}
{"type":"user","message":{"role":"user","content":"AFTER cutoff"},"uuid":"u2","timestamp":"2026-05-14T11:00:00.000Z","sessionId":"sid"}
`
	if err := os.WriteFile(jsonlPath, []byte(j), 0o644); err != nil {
		t.Fatal(err)
	}
	cutoff, _ := time.Parse(time.RFC3339Nano, "2026-05-14T10:00:00.000Z")

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	rc := renderTranscript(jsonlPath, false, cutoff)
	w.Close()
	os.Stdout = old
	if rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
	buf := make([]byte, 64*1024)
	n, _ := r.Read(buf)
	out := string(buf[:n])

	if !strings.Contains(out, "NO TIMESTAMP entry") {
		t.Errorf("entry without timestamp dropped (should be kept defensively):\n%s", out)
	}
	if strings.Contains(out, "BEFORE cutoff") {
		t.Errorf("entry with timestamp < cutoff was kept:\n%s", out)
	}
	if !strings.Contains(out, "AFTER cutoff") {
		t.Errorf("entry with timestamp >= cutoff was dropped:\n%s", out)
	}
}

// TestTranscriptKeepsEntriesWithUnparseableTimestamp pins the second
// defensive case: an entry whose `timestamp` field is present but not
// a valid RFC3339[Nano] is KEPT. Better over-inclusive than silently
// lossy.
func TestTranscriptKeepsEntriesWithUnparseableTimestamp(t *testing.T) {
	tmp := t.TempDir()
	jsonlPath := filepath.Join(tmp, "sid.jsonl")
	const j = `{"type":"user","message":{"role":"user","content":"BAD TIMESTAMP entry"},"uuid":"u0","timestamp":"not-a-date","sessionId":"sid"}
`
	if err := os.WriteFile(jsonlPath, []byte(j), 0o644); err != nil {
		t.Fatal(err)
	}
	cutoff, _ := time.Parse(time.RFC3339Nano, "2026-05-14T10:00:00.000Z")

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	rc := renderTranscript(jsonlPath, false, cutoff)
	w.Close()
	os.Stdout = old
	if rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
	buf := make([]byte, 64*1024)
	n, _ := r.Read(buf)
	out := string(buf[:n])

	if !strings.Contains(out, "BAD TIMESTAMP entry") {
		t.Errorf("entry with unparseable timestamp dropped (should be kept defensively):\n%s", out)
	}
}

// TestTranscriptRegularSpawnPassthrough pins the no-op property for
// non-`--here` tasks: their session_started ≈ first jsonl message
// (the binary pre-allocates the UUID before spawning the tab, so the
// first message arrives strictly after session_started). The filter
// should pass all entries through.
func TestTranscriptRegularSpawnPassthrough(t *testing.T) {
	tmp := t.TempDir()
	jsonlPath := filepath.Join(tmp, "sid.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(sampleSessionJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	// session_started slightly BEFORE the first jsonl entry (which is
	// the normal `flow do` ordering).
	cutoff, _ := time.Parse(time.RFC3339Nano, "2026-04-12T09:59:00.000Z")

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	rc := renderTranscript(jsonlPath, false, cutoff)
	w.Close()
	os.Stdout = old
	if rc != 0 {
		t.Fatalf("rc=%d, want 0", rc)
	}
	buf := make([]byte, 64*1024)
	n, _ := r.Read(buf)
	out := string(buf[:n])

	// All the same checks as TestTranscriptRender should pass — filter
	// is a no-op when session_started precedes everything.
	for _, want := range []string{"Hello, can you help me?", "Sure, let me check.", "Done!"} {
		if !strings.Contains(out, want) {
			t.Errorf("regular-spawn filter dropped %q; output:\n%s", want, out)
		}
	}
}

func TestTranscriptCommandCodexBackendUsesStoredPath(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "codex-transcript")
	tmp := t.TempDir()
	jsonlPath := filepath.Join(tmp, "stored-codex.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(sampleCodexSessionJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	db := openFlowDB(t)
	now := flowdb.NowISO()
	if err := flowdb.UpsertTaskSession(db, "codex-transcript", "codex", "codex-transcript-sid", jsonlPath, now, nil, now); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if rc := cmdTranscript([]string{"codex-transcript", "--backend", "codex"}); rc != 0 {
			t.Fatalf("rc=%d", rc)
		}
	})
	for _, want := range []string{"Codex hello", "Codex reply", "─── Tool: shell ───", "repo"} {
		if !strings.Contains(out, want) {
			t.Fatalf("codex transcript missing %q; got:\n%s", want, out)
		}
	}
}

func TestTranscriptBackendFlagSelectsBetweenSessions(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "dual-transcript")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "dual-transcript")
	if err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(home, ".claude", "projects", EncodeCwdForClaude(task.WorkDir))
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "claude-sid.jsonl"), []byte(sampleSessionJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	codexPath := filepath.Join(t.TempDir(), "codex.jsonl")
	if err := os.WriteFile(codexPath, []byte(sampleCodexSessionJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	now := flowdb.NowISO()
	if err := flowdb.UpsertTaskSession(db, "dual-transcript", "claude", "claude-sid", nil, now, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := flowdb.UpsertTaskSession(db, "dual-transcript", "codex", "codex-sid", codexPath, now, nil, now); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if rc := cmdTranscript([]string{"dual-transcript", "--backend", "codex"}); rc != 0 {
			t.Fatalf("codex rc=%d", rc)
		}
	})
	if !strings.Contains(out, "Codex reply") || strings.Contains(out, "Sure, let me check.") {
		t.Fatalf("--backend codex selected wrong transcript:\n%s", out)
	}
}
