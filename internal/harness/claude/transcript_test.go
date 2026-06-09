package claude

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sampleSessionJSONL is a minimal but representative session jsonl
// for testing the parser. It includes each record type the parser
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

// renderToString is a tiny test helper around RenderJSONL.
func renderToString(t *testing.T, raw string, compact bool) string {
	t.Helper()
	var buf bytes.Buffer
	if err := RenderJSONL(strings.NewReader(raw), compact, &buf); err != nil {
		t.Fatalf("RenderJSONL: %v", err)
	}
	return buf.String()
}

func TestRender(t *testing.T) {
	out := renderToString(t, sampleSessionJSONL, false)

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

	// Skipped types don't leak.
	if strings.Contains(out, "permission-mode") {
		t.Error("permission-mode record leaked into output")
	}
	if strings.Contains(out, "file-history-snapshot") {
		t.Error("file-history-snapshot record leaked into output")
	}
}

func TestRenderCompact(t *testing.T) {
	out := renderToString(t, sampleSessionJSONL, true)

	if !strings.Contains(out, "Hello, can you help me?") {
		t.Error("compact missing user message")
	}
	if !strings.Contains(out, "─── Tool: Bash ───") {
		t.Error("compact missing tool use")
	}
	if !strings.Contains(out, "Done!") {
		t.Error("compact missing assistant text")
	}

	if strings.Contains(out, "─── Thinking ───") {
		t.Error("compact should omit thinking blocks")
	}
	if strings.Contains(out, "─── Result") {
		t.Error("compact should omit tool results")
	}
}

// retroBindJSONL models a retrospective `flow do --here` bind: the
// whole conversation predates the bind, and there is no later "post-
// bind" turn. The renderer must surface every entry — this is the
// exact case the old session_started cutoff broke (it stamped the bind
// time after all of these and elided them).
const retroBindJSONL = `{"type":"user","message":{"role":"user","content":"PRE-BIND chatter"},"uuid":"u0","timestamp":"2026-05-14T10:00:00.000Z","sessionId":"sid"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"PRE-BIND reply"}]},"uuid":"a0","timestamp":"2026-05-14T10:00:01.000Z","sessionId":"sid"}
{"type":"user","message":{"role":"user","content":"MORE work, still before bind"},"uuid":"u1","timestamp":"2026-05-14T11:00:00.000Z","sessionId":"sid"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"MORE reply"}]},"uuid":"a1","timestamp":"2026-05-14T11:00:01.000Z","sessionId":"sid"}
`

// TestRendersWholeTranscript pins the no-cutoff contract: every
// user/assistant entry is rendered regardless of its timestamp, so a
// retrospective `--here` bind (session_started after all of these)
// still feeds the full conversation to the close-out sweep.
func TestRendersWholeTranscript(t *testing.T) {
	out := renderToString(t, retroBindJSONL, false)

	for _, want := range []string{"PRE-BIND chatter", "PRE-BIND reply", "MORE work, still before bind", "MORE reply"} {
		if !strings.Contains(out, want) {
			t.Errorf("entry dropped from full-render output; missing %q:\n%s", want, out)
		}
	}
}

// TestRendersEntriesWithoutTimestamp pins that entries lacking a
// `timestamp` field still render — the renderer never inspects
// timestamps, so a missing one is a non-event.
func TestRendersEntriesWithoutTimestamp(t *testing.T) {
	const j = `{"type":"user","message":{"role":"user","content":"NO TIMESTAMP entry"},"uuid":"u0","sessionId":"sid"}
`
	out := renderToString(t, j, false)

	if !strings.Contains(out, "NO TIMESTAMP entry") {
		t.Errorf("entry without timestamp dropped:\n%s", out)
	}
}

// TestResolveTranscriptPathPrefersDeterministic: when the cwd-encoded
// path exists, it wins — no glob needed.
func TestResolveTranscriptPathPrefersDeterministic(t *testing.T) {
	home := t.TempDir()
	cwd := "/Users/x/code/app"
	sid := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

	det := filepath.Join(home, ".claude", "projects", EncodeCwd(cwd), sid+".jsonl")
	if err := os.MkdirAll(filepath.Dir(det), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(det, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A decoy under a different project dir must NOT be chosen.
	decoyDir := filepath.Join(home, ".claude", "projects", "-Users-x-other")
	if err := os.MkdirAll(decoyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoyDir, sid+".jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveTranscriptPath(home, cwd, sid)
	if err != nil {
		t.Fatalf("resolveTranscriptPath: %v", err)
	}
	if got != det {
		t.Errorf("got %q, want deterministic path %q", got, det)
	}
}

// TestResolveTranscriptPathGlobFallback simulates the bg/worktree gotcha:
// the session ran with cwd under a git worktree but its jsonl is keyed
// under the PARENT repo's project dir. The cwd-encoded path doesn't
// exist, so resolution must fall back to a glob on the globally-unique
// session id.
func TestResolveTranscriptPathGlobFallback(t *testing.T) {
	home := t.TempDir()
	worktreeCwd := "/Users/x/flow/.claude/worktrees/bg-spawn-backend"
	sid := "48d287d9-1ef0-4738-84b9-3110beb988c4"

	// jsonl lives under the parent-repo project dir, not the worktree's.
	parentDir := filepath.Join(home, ".claude", "projects", "-Users-x-flow")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	actual := filepath.Join(parentDir, sid+".jsonl")
	if err := os.WriteFile(actual, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveTranscriptPath(home, worktreeCwd, sid)
	if err != nil {
		t.Fatalf("resolveTranscriptPath (glob fallback): %v", err)
	}
	if got != actual {
		t.Errorf("got %q, want globbed path %q", got, actual)
	}
}

// TestResolveTranscriptPathNotFound: neither deterministic nor glob hits.
func TestResolveTranscriptPathNotFound(t *testing.T) {
	home := t.TempDir()
	_, err := resolveTranscriptPath(home, "/Users/x/code/app", "ffffffff-0000-4000-8000-000000000000")
	if err == nil {
		t.Fatalf("resolveTranscriptPath: want error when no jsonl exists")
	}
}
