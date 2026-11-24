package prx

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"flow/internal/harness"
)

// TranscriptEntries reads the id-keyed transcript (plain or gzipped) and
// decodes it into normalised entries; cwd is ignored, as in
// RenderTranscript.
func (p *prx) TranscriptEntries(_ string, sessionID string, compact bool) ([]harness.TranscriptEntry, error) {
	path, err := transcriptPath(sessionID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open prx transcript %s: %w", path, err)
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("decompress prx transcript %s: %w", path, err)
		}
		defer gz.Close()
		r = gz
	}
	return DecodeJSONL(r, compact)
}

const maxEntryLen = 500

// DecodeJSONL decodes prx `message` entries into normalised entries — the
// structured counterpart of RenderJSONL.
func DecodeJSONL(r io.Reader, compact bool) ([]harness.TranscriptEntry, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	out := []harness.TranscriptEntry{}
	for scanner.Scan() {
		var entry struct {
			Type      string          `json:"type"`
			Timestamp json.RawMessage `json:"timestamp"`
			Message   message         `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Type != "message" {
			continue
		}
		ts := entryTimestamp(entry.Timestamp)
		m := entry.Message
		switch m.Role {
		case "user":
			if m.Text != "" {
				out = append(out, harness.TranscriptEntry{TS: ts, Role: harness.RoleUser, Kind: harness.KindText, Text: m.Text})
			}
			for _, b := range m.Content {
				if b.Type == "text" && b.Text != "" {
					out = append(out, harness.TranscriptEntry{TS: ts, Role: harness.RoleUser, Kind: harness.KindText, Text: b.Text})
				}
			}
		case "assistant":
			for _, b := range m.Content {
				e := harness.TranscriptEntry{TS: ts, Role: harness.RoleAssistant}
				switch {
				case b.Type == "text" && b.Text != "":
					e.Kind, e.Text = harness.KindText, b.Text
				case b.Type == "thinking" && b.Text != "" && !compact:
					e.Kind, e.Text = harness.KindThinking, b.Text
				case b.Type == "toolCall":
					e.Kind, e.Tool, e.Text = harness.KindToolCall, b.Name, truncate(string(b.Arguments), maxEntryLen)
				default:
					continue
				}
				out = append(out, e)
			}
		case "toolResult":
			if compact {
				continue
			}
			var parts []string
			for _, b := range m.Content {
				if b.Type == "text" && b.Text != "" {
					parts = append(parts, b.Text)
				}
			}
			out = append(out, harness.TranscriptEntry{
				TS: ts, Role: harness.RoleTool, Kind: harness.KindToolResult, Tool: m.ToolName,
				Text: truncate(strings.Join(parts, "\n"), maxEntryLen), IsError: m.IsError,
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read prx transcript: %w", err)
	}
	return out, nil
}

// entryTimestamp accepts the entry's timestamp as an RFC3339 string or as
// epoch milliseconds; anything else is "".
func entryTimestamp(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var ms int64
	if json.Unmarshal(raw, &ms) == nil && ms > 0 {
		return time.UnixMilli(ms).UTC().Format(time.RFC3339)
	}
	return ""
}
