package claude

import (
	"bytes"
	"strings"
	"testing"
	"time"
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
func renderToString(t *testing.T, raw string, compact bool, cutoff time.Time) string {
	t.Helper()
	var buf bytes.Buffer
	if err := RenderJSONL(strings.NewReader(raw), compact, cutoff, &buf); err != nil {
		t.Fatalf("RenderJSONL: %v", err)
	}
	return buf.String()
}

func TestRender(t *testing.T) {
	out := renderToString(t, sampleSessionJSONL, false, time.Time{})

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
	out := renderToString(t, sampleSessionJSONL, true, time.Time{})

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

// preBindJSONL has two user messages: one before session_started
// (pre-bind chatter from the dispatch session) and one after. The
// cutoff filter should drop the pre-bind entry but keep the post-bind
// one.
const preBindJSONL = `{"type":"user","message":{"role":"user","content":"PRE-BIND chatter"},"uuid":"u0","timestamp":"2026-05-14T10:00:00.000Z","sessionId":"sid"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"PRE-BIND reply"}]},"uuid":"a0","timestamp":"2026-05-14T10:00:01.000Z","sessionId":"sid"}
{"type":"user","message":{"role":"user","content":"POST-BIND request"},"uuid":"u1","timestamp":"2026-05-14T11:00:00.000Z","sessionId":"sid"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"POST-BIND reply"}]},"uuid":"a1","timestamp":"2026-05-14T11:00:01.000Z","sessionId":"sid"}
`

// TestFiltersByCutoff pins the --here close-out sweep contract:
// entries with timestamp < cutoff are dropped, entries at-or-after
// are kept.
func TestFiltersByCutoff(t *testing.T) {
	cutoff, _ := time.Parse(time.RFC3339Nano, "2026-05-14T10:30:00.000Z")
	out := renderToString(t, preBindJSONL, false, cutoff)

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

// TestZeroCutoffNoFilter pins the no-op case: when cutoff is the
// zero time.Time, the filter is bypassed and all entries pass.
func TestZeroCutoffNoFilter(t *testing.T) {
	out := renderToString(t, preBindJSONL, false, time.Time{})

	if !strings.Contains(out, "PRE-BIND chatter") {
		t.Errorf("zero cutoff should pass everything; pre-bind missing:\n%s", out)
	}
	if !strings.Contains(out, "POST-BIND request") {
		t.Errorf("zero cutoff should pass everything; post-bind missing:\n%s", out)
	}
}

// TestKeepsEntriesWithMissingTimestamp pins the defensive behavior:
// an entry without a `timestamp` field is KEPT regardless of cutoff.
// Don't silently lose data.
func TestKeepsEntriesWithMissingTimestamp(t *testing.T) {
	const j = `{"type":"user","message":{"role":"user","content":"NO TIMESTAMP entry"},"uuid":"u0","sessionId":"sid"}
{"type":"user","message":{"role":"user","content":"BEFORE cutoff"},"uuid":"u1","timestamp":"2026-05-14T09:00:00.000Z","sessionId":"sid"}
{"type":"user","message":{"role":"user","content":"AFTER cutoff"},"uuid":"u2","timestamp":"2026-05-14T11:00:00.000Z","sessionId":"sid"}
`
	cutoff, _ := time.Parse(time.RFC3339Nano, "2026-05-14T10:00:00.000Z")
	out := renderToString(t, j, false, cutoff)

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

// TestKeepsEntriesWithUnparseableTimestamp pins the second defensive
// case: an entry whose `timestamp` field is present but not a valid
// RFC3339[Nano] is KEPT. Better over-inclusive than silently lossy.
func TestKeepsEntriesWithUnparseableTimestamp(t *testing.T) {
	const j = `{"type":"user","message":{"role":"user","content":"BAD TIMESTAMP entry"},"uuid":"u0","timestamp":"not-a-date","sessionId":"sid"}
`
	cutoff, _ := time.Parse(time.RFC3339Nano, "2026-05-14T10:00:00.000Z")
	out := renderToString(t, j, false, cutoff)

	if !strings.Contains(out, "BAD TIMESTAMP entry") {
		t.Errorf("entry with unparseable timestamp dropped (should be kept defensively):\n%s", out)
	}
}

// TestRegularSpawnPassthrough pins the no-op property for non-`--here`
// tasks: their session_started ≈ first jsonl message (the binary pre-
// allocates the UUID before spawning, so first message lands strictly
// after session_started). The filter should pass all entries through.
func TestRegularSpawnPassthrough(t *testing.T) {
	cutoff, _ := time.Parse(time.RFC3339Nano, "2026-04-12T09:59:00.000Z")
	out := renderToString(t, sampleSessionJSONL, false, cutoff)

	for _, want := range []string{"Hello, can you help me?", "Sure, let me check.", "Done!"} {
		if !strings.Contains(out, want) {
			t.Errorf("regular-spawn filter dropped %q; output:\n%s", want, out)
		}
	}
}
