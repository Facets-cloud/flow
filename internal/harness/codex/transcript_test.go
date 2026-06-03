package codex

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

const sampleJSONL = `{"timestamp":"2026-05-25T10:00:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"please inspect"}]}}
{"timestamp":"2026-05-25T10:00:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"thinking","text":"need readme"},{"type":"output_text","text":"I will inspect it."}]}}
{"timestamp":"2026-05-25T10:00:02Z","type":"response_item","payload":{"type":"function_call","name":"Read","arguments":{"file_path":"README.md"}}}
{"timestamp":"2026-05-25T10:00:03Z","type":"response_item","payload":{"type":"function_call_output","output":"readme contents"}}
`

func TestRenderJSONL(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderJSONL(strings.NewReader(sampleJSONL), false, time.Time{}, &buf); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{"─── User ───", "please inspect", "─── Thinking ───", "need readme", "─── Tool: Read ───", `"file_path":"README.md"`, "─── Result ───", "readme contents"} {
		if !strings.Contains(got, want) {
			t.Fatalf("render missing %q:\n%s", want, got)
		}
	}
}

func TestRenderJSONLCompactOmitsThinkingAndResults(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderJSONL(strings.NewReader(sampleJSONL), true, time.Time{}, &buf); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, notWant := range []string{"─── Thinking ───", "─── Result ───", "readme contents"} {
		if strings.Contains(got, notWant) {
			t.Fatalf("compact render should omit %q:\n%s", notWant, got)
		}
	}
	if !strings.Contains(got, "─── Tool: Read ───") {
		t.Fatalf("compact render should keep tool calls:\n%s", got)
	}
}

func TestRenderJSONLCutoff(t *testing.T) {
	var buf bytes.Buffer
	cutoff := time.Date(2026, 5, 25, 10, 0, 2, 0, time.UTC)
	if err := RenderJSONL(strings.NewReader(sampleJSONL), false, cutoff, &buf); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, "please inspect") || strings.Contains(got, "I will inspect it.") {
		t.Fatalf("cutoff did not filter early messages:\n%s", got)
	}
	if !strings.Contains(got, "README.md") || !strings.Contains(got, "readme contents") {
		t.Fatalf("cutoff removed later records:\n%s", got)
	}
}
