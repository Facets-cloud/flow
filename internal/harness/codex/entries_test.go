package codex

import (
	"strings"
	"testing"

	"flow/internal/harness"
)

func TestDecodeJSONLEntries(t *testing.T) {
	input := strings.Join([]string{
		`{"timestamp":"2026-11-06T00:00:00Z","type":"session_meta","payload":{"id":"` + testThreadID + `"}}`,
		`{"timestamp":"2026-11-06T00:00:01Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"hidden"}]}}`,
		`{"timestamp":"2026-11-06T00:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
		`{"timestamp":"2026-11-06T00:00:03Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`,
		`{"timestamp":"2026-11-06T00:00:04Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"c1","input":"*** Begin Patch"}}`,
		`{"timestamp":"2026-11-06T00:00:05Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":"ok"}}`,
		`{"timestamp":"2026-11-06T00:00:06Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c2","arguments":"{\"cmd\":\"ls\"}"}}`,
		`{"timestamp":"2026-11-06T00:00:07Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c2","output":"files"}}`,
	}, "\n")
	got, err := DecodeJSONL(strings.NewReader(input), false)
	if err != nil {
		t.Fatal(err)
	}
	want := []harness.TranscriptEntry{
		{TS: "2026-11-06T00:00:02Z", Role: "user", Kind: "text", Text: "hello"},
		{TS: "2026-11-06T00:00:03Z", Role: "assistant", Kind: "text", Text: "hi"},
		{TS: "2026-11-06T00:00:04Z", Role: "assistant", Kind: "tool_call", Tool: "apply_patch", Text: "*** Begin Patch"},
		{TS: "2026-11-06T00:00:05Z", Role: "tool", Kind: "tool_result", Tool: "apply_patch", Text: "ok"},
		{TS: "2026-11-06T00:00:06Z", Role: "assistant", Kind: "tool_call", Tool: "exec_command", Text: `{"cmd":"ls"}`},
		{TS: "2026-11-06T00:00:07Z", Role: "tool", Kind: "tool_result", Tool: "exec_command", Text: "files"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	compact, _ := DecodeJSONL(strings.NewReader(input), true)
	for _, e := range compact {
		if e.Kind == "tool_result" {
			t.Errorf("compact kept %+v", e)
		}
	}
}

var _ harness.TranscriptDecoder = (*codex)(nil)
