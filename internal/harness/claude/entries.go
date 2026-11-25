package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"flow/internal/harness"
)

// TranscriptEntries resolves the session jsonl the same way
// RenderTranscript does and decodes it into normalised entries.
func (c *claude) TranscriptEntries(cwd, sessionID string, compact bool) ([]harness.TranscriptEntry, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("no home dir: %w", err)
	}
	p, err := resolveTranscriptPath(home, cwd, sessionID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open claude transcript %s: %w", p, err)
	}
	defer f.Close()
	return DecodeJSONL(f, compact)
}

// DecodeJSONL is the structured counterpart of RenderJSONL: the same
// records, the same tool-input summaries and result truncation, emitted
// as harness.TranscriptEntry values instead of text.
func DecodeJSONL(r io.Reader, compact bool) ([]harness.TranscriptEntry, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	out := []harness.TranscriptEntry{}
	toolNames := map[string]string{} // tool_use id → tool name
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec jsonlRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.Type != "user" && rec.Type != "assistant" {
			continue
		}
		var msg jsonlMessage
		if err := json.Unmarshal(rec.Message, &msg); err != nil {
			continue
		}
		var plain string
		if json.Unmarshal(msg.Content, &plain) == nil {
			if rec.Type == "user" && plain != "" {
				out = append(out, harness.TranscriptEntry{TS: rec.Timestamp, Role: harness.RoleUser, Kind: harness.KindText, Text: plain})
			}
			continue
		}
		var blocks []contentBlock
		if err := json.Unmarshal(msg.Content, &blocks); err != nil {
			continue
		}
		for _, b := range blocks {
			e := harness.TranscriptEntry{TS: rec.Timestamp}
			switch {
			case rec.Type == "user" && b.Type == "text" && b.Text != "":
				e.Role, e.Kind, e.Text = harness.RoleUser, harness.KindText, b.Text
			case rec.Type == "user" && b.Type == "tool_result":
				if compact {
					continue
				}
				e.Role, e.Kind, e.IsError = harness.RoleTool, harness.KindToolResult, b.IsError
				e.Tool = toolNames[b.ToolUseID]
				e.Text = toolResultText(b.Content)
			case rec.Type == "assistant" && b.Type == "text" && b.Text != "":
				e.Role, e.Kind, e.Text = harness.RoleAssistant, harness.KindText, b.Text
			case rec.Type == "assistant" && b.Type == "thinking" && b.Thinking != "":
				if compact {
					continue
				}
				e.Role, e.Kind, e.Text = harness.RoleAssistant, harness.KindThinking, b.Thinking
			case rec.Type == "assistant" && b.Type == "tool_use":
				if b.ID != "" {
					toolNames[b.ID] = b.Name
				}
				e.Role, e.Kind, e.Tool = harness.RoleAssistant, harness.KindToolCall, b.Name
				e.Text = formatToolInput(b.Name, b.Input)
			default:
				continue
			}
			out = append(out, e)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}
	return out, nil
}

// toolResultText flattens a tool_result's content: a string, or an array
// of content blocks whose text parts are joined by newlines. Like the text
// view, each part is truncated to maxToolResultLen on its own.
func toolResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return truncate(s, maxToolResultLen)
	}
	var inner []contentBlock
	if json.Unmarshal(raw, &inner) != nil {
		return ""
	}
	var text string
	for _, ib := range inner {
		if ib.Type == "text" && ib.Text != "" {
			if text != "" {
				text += "\n"
			}
			text += truncate(ib.Text, maxToolResultLen)
		}
	}
	return text
}
