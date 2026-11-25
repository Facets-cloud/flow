package prx

import (
	"strings"
	"testing"
	"unicode/utf8"

	"flow/internal/harness"
)

func TestDecodeJSONLEntries(t *testing.T) {
	in := `{"type":"session","version":3,"id":"x","cwd":"/w"}
{"type":"message","id":"b","timestamp":"2026-11-06T00:00:01Z","message":{"role":"user","text":"hello"}}
{"type":"message","id":"c","timestamp":"2026-11-06T00:00:02Z","message":{"role":"assistant","content":[{"type":"thinking","text":"hmm"},{"type":"text","text":"hi"},{"type":"toolCall","id":"t1","name":"bash","arguments":{"command":"ls"}}]}}
{"type":"message","id":"d","timestamp":"2026-11-06T00:00:03Z","message":{"role":"toolResult","toolCallId":"t1","toolName":"bash","isError":true,"content":[{"type":"text","text":"boom"}]}}
`
	got, err := DecodeJSONL(strings.NewReader(in), false)
	if err != nil {
		t.Fatal(err)
	}
	want := []harness.TranscriptEntry{
		{TS: "2026-11-06T00:00:01Z", Role: "user", Kind: "text", Text: "hello"},
		{TS: "2026-11-06T00:00:02Z", Role: "assistant", Kind: "thinking", Text: "hmm"},
		{TS: "2026-11-06T00:00:02Z", Role: "assistant", Kind: "text", Text: "hi"},
		{TS: "2026-11-06T00:00:02Z", Role: "assistant", Kind: "tool_call", Tool: "bash", Text: `{"command":"ls"}`},
		{TS: "2026-11-06T00:00:03Z", Role: "tool", Kind: "tool_result", Tool: "bash", Text: "boom", IsError: true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	compact, _ := DecodeJSONL(strings.NewReader(in), true)
	for _, e := range compact {
		if e.Kind == "thinking" || e.Kind == "tool_result" {
			t.Errorf("compact kept %+v", e)
		}
	}
}

var _ harness.TranscriptDecoder = (*prx)(nil)

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
