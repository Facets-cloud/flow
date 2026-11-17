package codex

import (
	"bufio"
	"encoding/json"
	"io"

	"flow/internal/harness"
)

// NormalizeLine decodes one Codex rollout line into normalized records.
// Only response_item records carry conversational content; session meta,
// turn context and provider events return nil.
func (c *codex) NormalizeLine(line []byte) ([]harness.NormalizedRecord, error) {
	var rec struct {
		Timestamp string          `json:"timestamp"`
		Type      string          `json:"type"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(line, &rec); err != nil || rec.Type != "response_item" {
		return nil, nil
	}
	var item struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Content []contentBlock  `json:"content"`
		Name    string          `json:"name"`
		Input   json.RawMessage `json:"input"`
		Output  json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(rec.Payload, &item); err != nil {
		return nil, nil
	}
	ts := rec.Timestamp
	switch item.Type {
	case "message":
		role := harness.RoleUser
		injected := false
		switch item.Role {
		case "assistant":
			role = harness.RoleAssistant
		case "developer", "system":
			// Codex injects instruction/context turns as developer/system
			// messages — flagged (not dropped) per the schema.
			role = harness.RoleSystem
			injected = true
		}
		var out []harness.NormalizedRecord
		for _, b := range item.Content {
			if b.Text == "" {
				continue
			}
			out = append(out, harness.NormalizedRecord{TS: ts, Role: role, Text: b.Text, Injected: injected})
		}
		return out, nil
	case "custom_tool_call":
		return []harness.NormalizedRecord{{
			TS: ts, Role: harness.RoleToolCall, Tool: item.Name, Text: formatJSON(item.Input),
		}}, nil
	case "custom_tool_call_output":
		return []harness.NormalizedRecord{{
			TS: ts, Role: harness.RoleToolResult, Text: formatJSON(item.Output),
		}}, nil
	}
	return nil, nil
}

// NormalizeStream scans a whole Codex rollout jsonl (backfill / tests).
func (c *codex) NormalizeStream(r io.Reader) ([]harness.NormalizedRecord, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var out []harness.NormalizedRecord
	for sc.Scan() {
		recs, _ := c.NormalizeLine(sc.Bytes())
		out = append(out, recs...)
	}
	return out, sc.Err()
}
