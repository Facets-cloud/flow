package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"flow/internal/agents"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type jsonlRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type jsonlMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func sessionJSONLPath(task *flowdb.Task) (string, error) {
	if !task.SessionID.Valid || task.SessionID.String == "" {
		return "", errors.New("task has no session")
	}
	if task.SessionProvider == agents.ProviderCodex {
		path, err := agents.FindCodexSessionPathByID(task.SessionID.String)
		if err != nil {
			return "", fmt.Errorf("codex session file not found for %s: %w", task.SessionID.String, err)
		}
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	encoded := encodeCwdForClaude(task.WorkDir)
	path := filepath.Join(home, ".claude", "projects", encoded, task.SessionID.String+".jsonl")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("session file not found: %s", path)
	}
	return path, nil
}

func encodeCwdForClaude(cwd string) string {
	return strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace(cwd)
}

func parseTranscriptFile(path string) ([]TranscriptEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var entries []TranscriptEntry
	var offset int64
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		lineOffset := offset
		offset += int64(len(line)) + 1
		if len(line) == 0 {
			continue
		}
		parsed := parseTranscriptLine(line, lineOffset)
		entries = append(entries, parsed...)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

type transcriptUsageStats struct {
	TokensUsed    int
	TokensMax     int
	LastTimestamp string
}

type transcriptUsageRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
	Message   struct {
		Usage transcriptTokenUsage `json:"usage"`
	} `json:"message"`
}

type transcriptTokenUsage struct {
	InputTokens              int `json:"input_tokens"`
	CachedInputTokens        int `json:"cached_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	ReasoningOutputTokens    int `json:"reasoning_output_tokens"`
	TotalTokens              int `json:"total_tokens"`
	ModelContextWindow       int `json:"model_context_window"`
}

func sessionTranscriptUsageStats(path string) transcriptUsageStats {
	f, err := os.Open(path)
	if err != nil {
		return transcriptUsageStats{}
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var stats transcriptUsageStats
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec transcriptUsageRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		stats.LastTimestamp = laterTimestamp(stats.LastTimestamp, rec.Timestamp)
		if used := rec.Message.Usage.total(); used > 0 {
			stats.TokensUsed = used
		}
		if rec.Payload != nil {
			var payload struct {
				Type string `json:"type"`
				Info struct {
					LastTokenUsage     transcriptTokenUsage `json:"last_token_usage"`
					TotalTokenUsage    transcriptTokenUsage `json:"total_token_usage"`
					ModelContextWindow int                  `json:"model_context_window"`
				} `json:"info"`
			}
			if err := json.Unmarshal(rec.Payload, &payload); err == nil && payload.Type == "token_count" {
				if used := payload.Info.LastTokenUsage.total(); used > 0 {
					stats.TokensUsed = used
				} else if used := payload.Info.TotalTokenUsage.total(); used > 0 {
					stats.TokensUsed = used
				}
				if payload.Info.ModelContextWindow > 0 {
					stats.TokensMax = payload.Info.ModelContextWindow
				}
			}
		}
	}
	return stats
}

func (u transcriptTokenUsage) total() int {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.InputTokens +
		u.CachedInputTokens +
		u.CacheCreationInputTokens +
		u.CacheReadInputTokens +
		u.OutputTokens +
		u.ReasoningOutputTokens
}

func parseTranscriptLine(line []byte, offset int64) []TranscriptEntry {
	if entries := parseCodexTranscriptLine(line, offset); len(entries) > 0 {
		return entries
	}
	var rec jsonlRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil
	}
	switch rec.Type {
	case "user":
		return stampTranscriptEntries(parseUserRecord(rec.Message, offset), rec.Timestamp)
	case "assistant":
		return stampTranscriptEntries(parseAssistantRecord(rec.Message, offset), rec.Timestamp)
	default:
		return nil
	}
}

type codexTranscriptRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Payload   json.RawMessage `json:"payload"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Output    json.RawMessage `json:"output"`
	Action    struct {
		Command []string `json:"command"`
	} `json:"action"`
}

type codexTranscriptBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func parseCodexTranscriptLine(line []byte, offset int64) []TranscriptEntry {
	var rec codexTranscriptRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil
	}
	switch rec.Type {
	case "response_item":
		var payload codexTranscriptRecord
		if err := json.Unmarshal(rec.Payload, &payload); err != nil {
			return nil
		}
		if payload.Timestamp == "" {
			payload.Timestamp = rec.Timestamp
		}
		return stampTranscriptEntries(codexPayloadEntries(payload, offset), payload.Timestamp)
	case "message", "function_call", "function_call_output", "local_shell_call":
		return stampTranscriptEntries(codexPayloadEntries(rec, offset), rec.Timestamp)
	default:
		return nil
	}
}

func stampTranscriptEntries(entries []TranscriptEntry, timestamp string) []TranscriptEntry {
	if timestamp == "" {
		return entries
	}
	for i := range entries {
		if entries[i].Timestamp == "" {
			entries[i].Timestamp = timestamp
		}
	}
	return entries
}

func codexPayloadEntries(rec codexTranscriptRecord, offset int64) []TranscriptEntry {
	switch rec.Type {
	case "message":
		return codexMessageEntries(rec.Role, rec.Content, offset)
	case "function_call":
		return []TranscriptEntry{{
			Type:             "tool_use",
			ToolName:         rec.Name,
			ToolInputSummary: truncate(rec.Arguments, 220),
			ByteOffset:       offset,
		}}
	case "function_call_output":
		return []TranscriptEntry{{
			Type:           "tool_result",
			ToolResultText: truncate(rawJSONAsText(rec.Output), 500),
			ByteOffset:     offset,
		}}
	case "local_shell_call":
		return []TranscriptEntry{{
			Type:             "tool_use",
			ToolName:         "local_shell",
			ToolInputSummary: strings.Join(rec.Action.Command, " "),
			ByteOffset:       offset,
		}}
	default:
		return nil
	}
}

func codexMessageEntries(role string, raw json.RawMessage, offset int64) []TranscriptEntry {
	entryType := "assistant"
	if role == "user" {
		entryType = "user"
	}
	var blocks []codexTranscriptBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var entries []TranscriptEntry
		for _, block := range blocks {
			if block.Text == "" {
				continue
			}
			switch block.Type {
			case "input_text", "output_text", "text":
				entries = append(entries, TranscriptEntry{Type: entryType, Text: block.Text, ByteOffset: offset})
			}
		}
		return entries
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil && text != "" {
		return []TranscriptEntry{{Type: entryType, Text: text, ByteOffset: offset}}
	}
	return nil
}

func rawJSONAsText(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

func parseUserRecord(raw json.RawMessage, offset int64) []TranscriptEntry {
	var msg jsonlMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil
	}
	var plain string
	if err := json.Unmarshal(msg.Content, &plain); err == nil {
		return []TranscriptEntry{{Type: "user", Text: plain, ByteOffset: offset}}
	}
	var blocks []contentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil
	}
	var entries []TranscriptEntry
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				entries = append(entries, TranscriptEntry{Type: "user", Text: b.Text, ByteOffset: offset})
			}
		case "tool_result":
			entries = append(entries, TranscriptEntry{
				Type:           "tool_result",
				ToolResultText: toolResultText(b),
				IsError:        b.IsError,
				ByteOffset:     offset,
			})
		}
	}
	return entries
}

func parseAssistantRecord(raw json.RawMessage, offset int64) []TranscriptEntry {
	var msg jsonlMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil
	}
	var entries []TranscriptEntry
	for _, b := range blocks {
		switch b.Type {
		case "thinking":
			if b.Thinking != "" {
				entries = append(entries, TranscriptEntry{Type: "thinking", Text: b.Thinking, ByteOffset: offset})
			}
		case "text":
			if b.Text != "" {
				entries = append(entries, TranscriptEntry{Type: "assistant", Text: b.Text, ByteOffset: offset})
			}
		case "tool_use":
			entries = append(entries, TranscriptEntry{
				Type:             "tool_use",
				ToolName:         b.Name,
				ToolInputSummary: formatToolInput(b.Name, b.Input),
				ByteOffset:       offset,
			})
		}
	}
	return entries
}

func toolResultText(b contentBlock) string {
	var text string
	if err := json.Unmarshal(b.Content, &text); err == nil {
		return text
	}
	var blocks []contentBlock
	if err := json.Unmarshal(b.Content, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func formatToolInput(name string, raw json.RawMessage) string {
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return string(raw)
	}
	switch name {
	case "Bash":
		if cmd, ok := m["command"].(string); ok {
			return "$ " + cmd
		}
	case "Read", "Write", "Edit":
		if fp, ok := m["file_path"].(string); ok {
			return fp
		}
	case "Glob":
		if p, ok := m["pattern"].(string); ok {
			return p
		}
	case "Grep":
		if p, ok := m["pattern"].(string); ok {
			if path, ok := m["path"].(string); ok {
				return p + " in " + path
			}
			return p
		}
	}
	compact, err := json.Marshal(m)
	if err != nil {
		return string(raw)
	}
	return truncate(string(compact), 220)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
