package praxis

import (
	"strings"
	"testing"
)

// TestRenderJSONLRealPraxisShape covers both message encodings persisted by
// Praxis: user messages use message.text, while assistant messages use an
// array of typed content parts. Session headers and non-text tool parts are
// intentionally not rendered.
func TestRenderJSONLRealPraxisShape(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"session","version":3,"id":"019fb187-1e98-7ec5-aab5-d3b788b1fe55"}`,
		`{"type":"message","message":{"role":"user","text":"Please inspect the task."}}`,
		`{"type":"message","message":{"role":"assistant","content":[{"type":"thinking","text":"private"},{"type":"text","text":"I found the issue."},{"type":"tool_use","name":"read"}]}}`,
		`{"type":"summary","summary":"ignored"}`,
	}, "\n")

	var out strings.Builder
	if err := renderJSONL(strings.NewReader(transcript), false, &out); err != nil {
		t.Fatalf("renderJSONL: %v", err)
	}
	got := out.String()
	want := "user: Please inspect the task.\n\nassistant: I found the issue.\n\n"
	if got != want {
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
	if got, want := out.String(), "user: kept\n\n"; got != want {
		t.Errorf("renderJSONL() = %q, want %q", got, want)
	}
}
