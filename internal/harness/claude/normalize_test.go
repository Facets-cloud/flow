package claude

import (
	"strings"
	"testing"

	"flow/internal/harness"
)

// A claude jsonl exercising every record shape the normalizer handles:
// a typed user string, an assistant turn with thinking + text + tool_use,
// and a user tool_result.
const claudeFixture = `{"type":"user","timestamp":"2026-11-17T10:00:00Z","message":{"role":"user","content":"add oauth login"}}
{"type":"assistant","timestamp":"2026-11-17T10:00:01Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"let me think"},{"type":"text","text":"on it"},{"type":"tool_use","name":"Bash","id":"t1","input":{"command":"ls"}}]}}
{"type":"user","timestamp":"2026-11-17T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"file.go"}]}}
{"type":"summary","summary":"ignore me"}`

func TestClaudeNormalizeStream(t *testing.T) {
	c := New().(*claude)
	recs, err := c.NormalizeStream(strings.NewReader(claudeFixture))
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		role, text, tool string
	}{
		{harness.RoleUser, "add oauth login", ""},
		{harness.RoleAssistant, "let me think", ""},
		{harness.RoleAssistant, "on it", ""},
		{harness.RoleToolCall, "$ ls", "Bash"},
		{harness.RoleToolResult, "file.go", ""},
	}
	if len(recs) != len(want) {
		t.Fatalf("got %d records, want %d: %+v", len(recs), len(want), recs)
	}
	for i, w := range want {
		if recs[i].Role != w.role || recs[i].Text != w.text || recs[i].Tool != w.tool {
			t.Errorf("rec %d = {role=%q text=%q tool=%q}, want {role=%q text=%q tool=%q}",
				i, recs[i].Role, recs[i].Text, recs[i].Tool, w.role, w.text, w.tool)
		}
	}
	if recs[0].TS != "2026-11-17T10:00:00Z" {
		t.Errorf("TS not carried through: %q", recs[0].TS)
	}
	for i, r := range recs {
		if r.Injected {
			t.Errorf("rec %d unexpectedly flagged injected: %+v", i, r)
		}
	}
}

// TestClaudeNormalizeFlagsMetaAndSidechain pins #5: isMeta and isSidechain
// records are flagged injected (not a genuine human turn) so the capture
// layer does not count them as user turns.
func TestClaudeNormalizeFlagsMetaAndSidechain(t *testing.T) {
	c := New().(*claude)
	const fixture = `{"type":"user","isMeta":true,"timestamp":"2026-11-17T10:00:00Z","message":{"role":"user","content":"<local-command-stdout>noise</local-command-stdout>"}}
{"type":"user","isSidechain":true,"timestamp":"2026-11-17T10:00:01Z","message":{"role":"user","content":"sub-agent task prompt"}}
{"type":"assistant","isSidechain":true,"timestamp":"2026-11-17T10:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"sub-agent reply"}]}}
{"type":"user","timestamp":"2026-11-17T10:00:03Z","message":{"role":"user","content":"a real prompt"}}`
	recs, err := c.NormalizeStream(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 4 {
		t.Fatalf("got %d records, want 4: %+v", len(recs), recs)
	}
	for i := 0; i < 3; i++ {
		if !recs[i].Injected {
			t.Errorf("meta/sidechain rec %d should be injected: %+v", i, recs[i])
		}
	}
	if recs[3].Injected {
		t.Errorf("the genuine user turn must NOT be injected: %+v", recs[3])
	}
}
