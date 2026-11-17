package codex

import (
	"strings"
	"testing"

	"flow/internal/harness"
)

const codexFixture = `{"timestamp":"2026-11-17T10:00:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"add oauth login"}]}}
{"timestamp":"2026-11-17T10:00:01Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"text","text":"injected instructions"}]}}
{"timestamp":"2026-11-17T10:00:02Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"on it"}]}}
{"timestamp":"2026-11-17T10:00:03Z","type":"response_item","payload":{"type":"custom_tool_call","name":"shell","input":"ls"}}
{"timestamp":"2026-11-17T10:00:04Z","type":"response_item","payload":{"type":"custom_tool_call_output","output":"file.go"}}
{"type":"session_meta","payload":{"id":"x"}}`

func TestCodexNormalizeStream(t *testing.T) {
	c := New().(*codex)
	recs, err := c.NormalizeStream(strings.NewReader(codexFixture))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		role, text, tool string
		injected         bool
	}{
		{harness.RoleUser, "add oauth login", "", false},
		{harness.RoleSystem, "injected instructions", "", true},
		{harness.RoleAssistant, "on it", "", false},
		{harness.RoleToolCall, "ls", "shell", false},
		{harness.RoleToolResult, "file.go", "", false},
	}
	if len(recs) != len(want) {
		t.Fatalf("got %d records, want %d: %+v", len(recs), len(want), recs)
	}
	for i, w := range want {
		if recs[i].Role != w.role || recs[i].Text != w.text || recs[i].Tool != w.tool || recs[i].Injected != w.injected {
			t.Errorf("rec %d = %+v, want {role=%q text=%q tool=%q injected=%v}",
				i, recs[i], w.role, w.text, w.tool, w.injected)
		}
	}
}
