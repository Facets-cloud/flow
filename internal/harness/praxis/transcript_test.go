package praxis

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// realTranscript uses the exact shapes praxis persists — verified against
// ~/.praxis/agent/sessions/<id>/session.jsonl. Note the camelCase part
// types ("toolCall") and that a tool result is its own message with
// role "toolResult", not a part of the assistant turn.
const realTranscript = `{"type":"session","version":3,"id":"019fb187-1e98-7ec5-aab5-d3b788b1fe55","cwd":"/tmp/x"}
{"type":"message","message":{"role":"user","text":"Please inspect the task."}}
{"type":"message","message":{"role":"assistant","content":[{"type":"thinking","text":"private reasoning"},{"type":"text","text":"I found the issue."},{"type":"toolCall","id":"toolu_1","name":"bash","arguments":{"command":"go test ./...","timeout":60}}]}}
{"type":"message","message":{"role":"toolResult","toolCallId":"toolu_1","toolName":null,"content":[{"type":"text","text":"ok  flow/internal/app"}]}}
{"type":"todo_update","todos":[]}`

// Non-compact keeps everything: thinking, assistant text, the tool call
// and its result, in the same section vocabulary the claude adapter uses.
func TestRenderJSONLFullKeepsThinkingToolsAndResults(t *testing.T) {
	var out strings.Builder
	if err := renderJSONL(strings.NewReader(realTranscript), false, &out); err != nil {
		t.Fatalf("renderJSONL: %v", err)
	}
	want := strings.Join([]string{
		"─── User ───",
		"Please inspect the task.",
		"",
		"─── Thinking ───",
		"private reasoning",
		"─── Assistant ───",
		"I found the issue.",
		"─── Tool: bash ───",
		"$ go test ./...",
		"",
		"─── Result ───",
		"ok  flow/internal/app",
		"",
	}, "\n")
	if got := out.String(); got != want {
		t.Errorf("renderJSONL(compact=false) =\n%q\nwant\n%q", got, want)
	}
}

// Compact drops thinking and tool results — the bulk of a session — but
// keeps the record of which tools ran.
func TestRenderJSONLCompactDropsThinkingAndResults(t *testing.T) {
	var out strings.Builder
	if err := renderJSONL(strings.NewReader(realTranscript), true, &out); err != nil {
		t.Fatalf("renderJSONL: %v", err)
	}
	got := out.String()
	want := strings.Join([]string{
		"─── User ───",
		"Please inspect the task.",
		"",
		"─── Assistant ───",
		"I found the issue.",
		"─── Tool: bash ───",
		"$ go test ./...",
		"",
	}, "\n")
	if got != want {
		t.Errorf("renderJSONL(compact=true) =\n%q\nwant\n%q", got, want)
	}
	for _, leaked := range []string{"private reasoning", "ok  flow/internal/app", "Result"} {
		if strings.Contains(got, leaked) {
			t.Errorf("compact output leaked %q:\n%s", leaked, got)
		}
	}
}

// compact must actually change the output; a dead parameter silently
// broke both modes before.
func TestRenderJSONLCompactDiffersFromFull(t *testing.T) {
	var full, compact strings.Builder
	if err := renderJSONL(strings.NewReader(realTranscript), false, &full); err != nil {
		t.Fatalf("renderJSONL(full): %v", err)
	}
	if err := renderJSONL(strings.NewReader(realTranscript), true, &compact); err != nil {
		t.Fatalf("renderJSONL(compact): %v", err)
	}
	if full.String() == compact.String() {
		t.Error("compact and full renderings are identical; compact is being ignored")
	}
	if len(compact.String()) >= len(full.String()) {
		t.Errorf("compact rendering (%d bytes) is not smaller than full (%d bytes)",
			len(compact.String()), len(full.String()))
	}
}

func TestRenderJSONLTruncatesLongToolResults(t *testing.T) {
	long := strings.Repeat("x", maxToolResultLen*2)
	transcript := `{"type":"message","message":{"role":"toolResult","content":[{"type":"text","text":"` + long + `"}]}}`
	var out strings.Builder
	if err := renderJSONL(strings.NewReader(transcript), false, &out); err != nil {
		t.Fatalf("renderJSONL: %v", err)
	}
	got := out.String()
	if !strings.HasSuffix(strings.TrimRight(got, "\n"), "...") {
		t.Errorf("long tool result was not truncated: %q", got)
	}
	if len(got) > maxToolResultLen+64 {
		t.Errorf("tool result rendering is %d bytes, want <= %d", len(got), maxToolResultLen+64)
	}
}

// A result message labels itself with toolName when praxis populates it.
func TestRenderJSONLLabelsResultWithToolName(t *testing.T) {
	transcript := `{"type":"message","message":{"role":"toolResult","toolName":"grep","content":[{"type":"text","text":"3 matches"}]}}`
	var out strings.Builder
	if err := renderJSONL(strings.NewReader(transcript), false, &out); err != nil {
		t.Fatalf("renderJSONL: %v", err)
	}
	if got, want := out.String(), "─── Result: grep ───\n3 matches\n"; got != want {
		t.Errorf("renderJSONL() = %q, want %q", got, want)
	}
}

// Content is normally an array but the decoder also accepts a bare
// string, which the previous decoder silently dropped.
func TestRenderJSONLAcceptsStringContent(t *testing.T) {
	transcript := `{"type":"message","message":{"role":"assistant","content":"plain string body"}}`
	var out strings.Builder
	if err := renderJSONL(strings.NewReader(transcript), false, &out); err != nil {
		t.Fatalf("renderJSONL: %v", err)
	}
	if got, want := out.String(), "─── Assistant ───\nplain string body\n"; got != want {
		t.Errorf("renderJSONL() = %q, want %q", got, want)
	}
}

func TestRenderJSONLSkipsMalformedLines(t *testing.T) {
	transcript := "not json\n" +
		`{"type":"message","message":{"role":"user","text":"kept"}}` + "\n"
	var out strings.Builder
	if err := renderJSONL(strings.NewReader(transcript), false, &out); err != nil {
		t.Fatalf("renderJSONL: %v", err)
	}
	if got, want := out.String(), "─── User ───\nkept\n"; got != want {
		t.Errorf("renderJSONL() = %q, want %q", got, want)
	}
}

// Records that render to nothing must not leave a stray blank line.
func TestRenderJSONLNoStraySeparators(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"message","message":{"role":"user","text":"first"}}`,
		`{"type":"message","message":{"role":"assistant","content":[]}}`,
		`{"type":"message","message":{"role":"toolResult","content":[{"type":"text","text":"hidden"}]}}`,
		`{"type":"message","message":{"role":"user","text":"second"}}`,
	}, "\n")
	var out strings.Builder
	if err := renderJSONL(strings.NewReader(transcript), true, &out); err != nil {
		t.Fatalf("renderJSONL: %v", err)
	}
	want := "─── User ───\nfirst\n\n─── User ───\nsecond\n"
	if got := out.String(); got != want {
		t.Errorf("renderJSONL() = %q, want %q", got, want)
	}
}

func TestFormatToolArgs(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{"bash command", `{"command":"ls -la","timeout":30}`, "$ ls -la"},
		{"grep prefers pattern", `{"glob":"*.go","path":"internal","pattern":"func New"}`, "func New"},
		{"read path", `{"path":"main.go","offset":10}`, "main.go"},
		{"web search query", `{"query":"praxis cli"}`, "praxis cli"},
		{"no known key falls back to raw", `{"language":"js","code":"1+1"}`, `{"language":"js","code":"1+1"}`},
		{"absent arguments", ``, ""},
		{"non-object arguments", `"bare"`, `"bare"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatToolArgs([]byte(tt.args)); got != tt.want {
				t.Errorf("formatToolArgs(%s) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestFormatToolArgsBoundsFallback(t *testing.T) {
	raw := `{"content":"` + strings.Repeat("y", maxToolArgsLen*3) + `"}`
	got := formatToolArgs([]byte(raw))
	if len(got) > maxToolArgsLen+3 {
		t.Errorf("fallback summary is %d bytes, want <= %d", len(got), maxToolArgsLen+3)
	}
}

// truncate must not split a multi-byte rune, which would emit invalid
// UTF-8 into the rendered transcript.
func TestTruncateIsRuneSafe(t *testing.T) {
	s := strings.Repeat("é", 10) // 2 bytes per rune
	got := truncate(s, 5)
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("truncate(%q, 5) = %q, want an ellipsis", s, got)
	}
	body := strings.TrimSuffix(got, "...")
	if len(body) != 4 {
		t.Errorf("truncate cut to %d bytes, want 4 (rune boundary below 5)", len(body))
	}
	if !utf8.ValidString(got) {
		t.Errorf("truncate produced invalid UTF-8: %q", got)
	}
}

func TestTruncateShortStringUnchanged(t *testing.T) {
	if got := truncate("abc", 10); got != "abc" {
		t.Errorf("truncate(\"abc\", 10) = %q, want \"abc\"", got)
	}
}
