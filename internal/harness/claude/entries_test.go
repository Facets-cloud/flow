package claude

import (
	"strings"
	"testing"
	"unicode/utf8"

	"flow/internal/harness"
)

func TestDecodeJSONLEntries(t *testing.T) {
	got, err := DecodeJSONL(strings.NewReader(sampleSessionJSONL), false)
	if err != nil {
		t.Fatal(err)
	}
	want := []harness.TranscriptEntry{
		{TS: "2026-04-12T10:00:00.000Z", Role: "user", Kind: "text", Text: "Hello, can you help me?"},
		{TS: "2026-04-12T10:00:01.000Z", Role: "assistant", Kind: "thinking", Text: "The user wants help."},
		{TS: "2026-04-12T10:00:01.000Z", Role: "assistant", Kind: "text", Text: "Sure, let me check."},
		{TS: "2026-04-12T10:00:02.000Z", Role: "assistant", Kind: "tool_call", Tool: "Bash", Text: "$ echo hello"},
		{TS: "2026-04-12T10:00:03.000Z", Role: "tool", Kind: "tool_result", Tool: "Bash", Text: "hello\n"},
		{TS: "2026-04-12T10:00:04.000Z", Role: "assistant", Kind: "tool_call", Tool: "Read", Text: "/tmp/test.go offset=10 limit=20"},
		{TS: "2026-04-12T10:00:05.000Z", Role: "tool", Kind: "tool_result", Tool: "Read", Text: "package main\n"},
		{TS: "2026-04-12T10:00:06.000Z", Role: "assistant", Kind: "text", Text: "Done!"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	compact, err := DecodeJSONL(strings.NewReader(sampleSessionJSONL), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range compact {
		if e.Kind == "thinking" || e.Kind == "tool_result" {
			t.Errorf("compact kept %+v", e)
		}
	}
}

// Tool results carry the tool name from the matching tool_use and the
// error flag; long results are truncated like the text render.
func TestDecodeJSONLToolResultNameAndError(t *testing.T) {
	long := strings.Repeat("x", 2000)
	in := `{"type":"assistant","timestamp":"t1","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu9","name":"Bash","input":{"command":"false"}}]}}
{"type":"user","timestamp":"t2","message":{"role":"user","content":[{"tool_use_id":"tu9","type":"tool_result","content":[{"type":"text","text":"` + long + `"}],"is_error":true}]}}
`
	got, err := DecodeJSONL(strings.NewReader(in), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("entries = %+v", got)
	}
	r := got[1]
	if r.Tool != "Bash" || !r.IsError || r.Role != "tool" || len(r.Text) > maxToolResultLen+3 {
		t.Errorf("result entry = %+v (len %d)", r, len(r.Text))
	}
}

var _ harness.TranscriptDecoder = (*claude)(nil)

// CodeRabbit #200 (3rd review): like the text view, each text block of a
// multi-block tool result is truncated on its own, so JSON never drops a
// later block the text view shows.
func TestDecodeJSONLMultiBlockResultTruncatedPerBlock(t *testing.T) {
	a, b := strings.Repeat("a", 400), strings.Repeat("b", 400)
	in := `{"type":"user","timestamp":"t","message":{"role":"user","content":[{"tool_use_id":"x","type":"tool_result","content":[{"type":"text","text":"` + a + `"},{"type":"text","text":"` + b + `"}]}]}}
`
	got, err := DecodeJSONL(strings.NewReader(in), false)
	if err != nil || len(got) != 1 {
		t.Fatalf("entries = %+v err = %v", got, err)
	}
	if got[0].Text != a+"\n"+b {
		t.Errorf("text has len %d, want both 400-byte blocks", len(got[0].Text))
	}
}

// CodeRabbit #200 (4th review): truncation never splits a UTF-8 rune.
func TestTruncateKeepsUTF8Boundary(t *testing.T) {
	s := strings.Repeat("a", 499) + "€" + "tail"
	got := truncate(s, 500)
	if !utf8.ValidString(got) {
		t.Errorf("truncate produced invalid UTF-8: %q", got[len(got)-8:])
	}
	if !strings.HasPrefix(got, strings.Repeat("a", 499)) {
		t.Errorf("lost the ASCII prefix: %q", got)
	}
}
