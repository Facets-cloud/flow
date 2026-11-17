package prx

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"

	"flow/internal/harness"
)

const prxFixture = `{"type":"header","id":"a"}
{"type":"message","id":"b","message":{"role":"user","text":"add oauth login"}}
{"type":"message","id":"c","message":{"role":"assistant","content":[{"type":"thinking","text":"hmm"},{"type":"text","text":"hi"},{"type":"toolCall","id":"t1","name":"bash","arguments":{"command":"ls"}}]}}
{"type":"message","id":"d","message":{"role":"toolResult","toolCallId":"t1","toolName":"bash","isError":true,"content":[{"type":"text","text":"boom"}]}}`

func wantPrxRecords(t *testing.T, recs []harness.NormalizedRecord) {
	t.Helper()
	want := []struct {
		role, text, tool string
	}{
		{harness.RoleUser, "add oauth login", ""},
		{harness.RoleAssistant, "hmm", ""},
		{harness.RoleAssistant, "hi", ""},
		{harness.RoleToolCall, `{"command":"ls"}`, "bash"},
		{harness.RoleToolResult, "boom", "bash"},
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
}

func TestPrxNormalizeStream(t *testing.T) {
	p := New().(*prx)
	recs, err := p.NormalizeStream(strings.NewReader(prxFixture))
	if err != nil {
		t.Fatal(err)
	}
	wantPrxRecords(t, recs)
}

// A gzipped (idle-compacted) prx session must normalize the same way.
func TestPrxNormalizeStreamGzip(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write([]byte(prxFixture))
	gz.Close()

	p := New().(*prx)
	recs, err := p.NormalizeStream(&buf)
	if err != nil {
		t.Fatal(err)
	}
	wantPrxRecords(t, recs)
}
