package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"flow/internal/harness"
)

// TranscriptEntries resolves the rollout the same way RenderTranscript
// does and decodes it into normalised entries.
func (c *codex) TranscriptEntries(_ string, sessionID string, compact bool) ([]harness.TranscriptEntry, error) {
	root, err := codexHome()
	if err != nil {
		return nil, err
	}
	p, err := resolveTranscriptPath(filepath.Join(root, "sessions"), sessionID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open Codex transcript %s: %w", p, err)
	}
	defer f.Close()
	return DecodeJSONL(f, compact)
}

// maxEntryLen bounds tool inputs and outputs in a structured transcript.
const maxEntryLen = 500

// DecodeJSONL decodes a Codex rollout's response_item records (messages,
// custom and function tool calls and their outputs) into normalised
// entries. Developer messages and session bookkeeping are skipped, as in
// RenderJSONL.
func DecodeJSONL(r io.Reader, compact bool) ([]harness.TranscriptEntry, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	out := []harness.TranscriptEntry{}
	toolNames := map[string]string{} // call_id → tool name
	for scanner.Scan() {
		var rec struct {
			Timestamp string          `json:"timestamp"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil || rec.Type != "response_item" {
			continue
		}
		var item struct {
			Type      string          `json:"type"`
			Role      string          `json:"role"`
			Content   []contentBlock  `json:"content"`
			Name      string          `json:"name"`
			CallID    string          `json:"call_id"`
			Input     json.RawMessage `json:"input"`
			Arguments json.RawMessage `json:"arguments"`
			Output    json.RawMessage `json:"output"`
		}
		if err := json.Unmarshal(rec.Payload, &item); err != nil {
			continue
		}
		switch item.Type {
		case "message":
			if item.Role == "developer" {
				continue
			}
			role := harness.RoleUser
			if item.Role == "assistant" {
				role = harness.RoleAssistant
			}
			for _, b := range item.Content {
				if b.Text != "" {
					out = append(out, harness.TranscriptEntry{TS: rec.Timestamp, Role: role, Kind: harness.KindText, Text: b.Text})
				}
			}
		case "custom_tool_call", "function_call":
			if item.CallID != "" {
				toolNames[item.CallID] = item.Name
			}
			in := item.Input
			if item.Type == "function_call" {
				in = item.Arguments
			}
			out = append(out, harness.TranscriptEntry{
				TS: rec.Timestamp, Role: harness.RoleAssistant, Kind: harness.KindToolCall,
				Tool: item.Name, Text: truncate(formatJSON(in), maxEntryLen),
			})
		case "custom_tool_call_output", "function_call_output":
			if compact {
				continue
			}
			out = append(out, harness.TranscriptEntry{
				TS: rec.Timestamp, Role: harness.RoleTool, Kind: harness.KindToolResult,
				Tool: toolNames[item.CallID], Text: truncate(formatJSON(item.Output), maxEntryLen),
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Codex session: %w", err)
	}
	return out, nil
}
