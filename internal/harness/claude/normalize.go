package claude

import (
	"bufio"
	"encoding/json"
	"io"

	"flow/internal/harness"
)

// NormalizeLine decodes one line of a Claude session jsonl into normalized
// records. One line can yield several records (an assistant message with
// text plus tool_use blocks). Unrecognized/bookkeeping lines return nil.
func (c *claude) NormalizeLine(line []byte) ([]harness.NormalizedRecord, error) {
	if len(line) == 0 {
		return nil, nil
	}
	var rec jsonlRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, nil // skip malformed lines, never abort a capture
	}
	var recs []harness.NormalizedRecord
	switch rec.Type {
	case "user":
		recs = normalizeUser(rec.Message, rec.Timestamp)
	case "assistant":
		recs = normalizeAssistant(rec.Message, rec.Timestamp)
	default:
		return nil, nil
	}
	// isMeta / isSidechain records are harness bookkeeping or sub-agent
	// (Task tool) turns, never the human typing — flag them injected so the
	// capture layer does not count them as user turns (which would skew turn
	// numbers and the note nudge). Flagged, not dropped, so the full copy
	// keeps the audit trail.
	if rec.IsMeta || rec.IsSidechain {
		for i := range recs {
			recs[i].Injected = true
		}
	}
	return recs, nil
}

// NormalizeStream scans a whole Claude jsonl byte-stream (used for backfill
// and tests).
func (c *claude) NormalizeStream(r io.Reader) ([]harness.NormalizedRecord, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var out []harness.NormalizedRecord
	for sc.Scan() {
		recs, _ := c.NormalizeLine(sc.Bytes())
		out = append(out, recs...)
	}
	return out, sc.Err()
}

func normalizeUser(raw json.RawMessage, ts string) []harness.NormalizedRecord {
	var msg jsonlMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil
	}
	// Content is a plain string (typed user message) or an array (tool
	// results, or text blocks).
	var plain string
	if err := json.Unmarshal(msg.Content, &plain); err == nil {
		if plain == "" {
			return nil
		}
		return []harness.NormalizedRecord{{TS: ts, Role: harness.RoleUser, Text: plain}}
	}
	var blocks []contentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil
	}
	var out []harness.NormalizedRecord
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				out = append(out, harness.NormalizedRecord{TS: ts, Role: harness.RoleUser, Text: b.Text})
			}
		case "tool_result":
			out = append(out, harness.NormalizedRecord{TS: ts, Role: harness.RoleToolResult, Text: toolResultText(b)})
		}
	}
	return out
}

func normalizeAssistant(raw json.RawMessage, ts string) []harness.NormalizedRecord {
	var msg jsonlMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil
	}
	var out []harness.NormalizedRecord
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				out = append(out, harness.NormalizedRecord{TS: ts, Role: harness.RoleAssistant, Text: b.Text})
			}
		case "thinking":
			if b.Thinking != "" {
				// Thinking is assistant reasoning; keep it (not compacted
				// here — the full copy is the point), flagged injected=false.
				out = append(out, harness.NormalizedRecord{TS: ts, Role: harness.RoleAssistant, Text: b.Thinking})
			}
		case "tool_use":
			out = append(out, harness.NormalizedRecord{
				TS:   ts,
				Role: harness.RoleToolCall,
				Tool: b.Name,
				Text: toolInputText(b.Name, b.Input),
			})
		}
	}
	return out
}

// toolResultText extracts the text of a tool_result block (string form or
// array-of-blocks form). Unlike the human renderer it does NOT truncate —
// the capture layer applies any configured cap.
func toolResultText(b contentBlock) string {
	var text string
	if err := json.Unmarshal(b.Content, &text); err == nil {
		return text
	}
	var inner []contentBlock
	if err := json.Unmarshal(b.Content, &inner); err != nil {
		return ""
	}
	var parts []string
	for _, ib := range inner {
		if ib.Type == "text" && ib.Text != "" {
			parts = append(parts, ib.Text)
		}
	}
	return joinLines(parts)
}

// toolInputText is the full (untruncated) tool input as compact JSON, with
// the same friendly one-liners the human renderer uses for common tools.
func toolInputText(name string, raw json.RawMessage) string {
	return formatToolInput(name, raw)
}

func joinLines(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\n"
		}
		out += p
	}
	return out
}
