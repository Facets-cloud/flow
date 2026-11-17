package prx

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"io"
	"strings"

	"flow/internal/harness"
)

// NormalizeLine decodes one prx transcript line into normalized records.
// Only `message` entries carry conversational content; header, model-change
// and compaction bookkeeping return nil.
func (p *prx) NormalizeLine(line []byte) ([]harness.NormalizedRecord, error) {
	var entry struct {
		Type    string  `json:"type"`
		TS      string  `json:"ts"`
		Message message `json:"message"`
	}
	if err := json.Unmarshal(line, &entry); err != nil || entry.Type != "message" {
		return nil, nil
	}
	m := entry.Message
	ts := entry.TS
	switch m.Role {
	case "user":
		var out []harness.NormalizedRecord
		if m.Text != "" {
			out = append(out, harness.NormalizedRecord{TS: ts, Role: harness.RoleUser, Text: m.Text})
		}
		for _, b := range m.Content {
			if b.Type == "text" && b.Text != "" {
				out = append(out, harness.NormalizedRecord{TS: ts, Role: harness.RoleUser, Text: b.Text})
			}
		}
		return out, nil
	case "assistant":
		var out []harness.NormalizedRecord
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				if b.Text != "" {
					out = append(out, harness.NormalizedRecord{TS: ts, Role: harness.RoleAssistant, Text: b.Text})
				}
			case "thinking":
				if b.Text != "" {
					out = append(out, harness.NormalizedRecord{TS: ts, Role: harness.RoleAssistant, Text: b.Text})
				}
			case "toolCall":
				out = append(out, harness.NormalizedRecord{
					TS: ts, Role: harness.RoleToolCall, Tool: b.Name, Text: string(b.Arguments),
				})
			}
		}
		return out, nil
	case "toolResult":
		var parts []string
		for _, b := range m.Content {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return []harness.NormalizedRecord{{
			TS: ts, Role: harness.RoleToolResult, Tool: m.ToolName, Text: strings.Join(parts, "\n"),
		}}, nil
	}
	return nil, nil
}

// NormalizeStream scans a whole prx transcript, transparently decompressing
// a gzipped (idle-compacted) session file.
func (p *prx) NormalizeStream(r io.Reader) ([]harness.NormalizedRecord, error) {
	br := bufio.NewReader(r)
	// Sniff the gzip magic so a .gz session (prx compresses idle sessions)
	// is handled without the caller knowing the on-disk form.
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		return p.normalizeReader(gz)
	}
	return p.normalizeReader(br)
}

func (p *prx) normalizeReader(r io.Reader) ([]harness.NormalizedRecord, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var out []harness.NormalizedRecord
	for sc.Scan() {
		recs, _ := p.NormalizeLine(sc.Bytes())
		out = append(out, recs...)
	}
	return out, sc.Err()
}
