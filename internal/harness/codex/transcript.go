package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (c *codex) RenderTranscript(cwd, sessionID string, compact bool, cutoff time.Time, w io.Writer) error {
	p, err := FindTranscript(sessionID)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return fmt.Errorf("open codex transcript %s: %w", p, err)
	}
	defer f.Close()
	return RenderJSONL(f, compact, cutoff, w)
}

func FindTranscript(sessionID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	root := filepath.Join(home, ".codex", "sessions")
	var found string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || found != "" {
			return nil
		}
		if strings.HasSuffix(path, ".jsonl") && strings.Contains(filepath.Base(path), sessionID) {
			found = path
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk codex sessions: %w", err)
	}
	if found != "" {
		return found, nil
	}
	return "", fmt.Errorf("codex transcript not found for session %s under %s", sessionID, root)
}

func RenderJSONL(r io.Reader, compact bool, cutoff time.Time, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	first := true
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if !cutoff.IsZero() {
			if ts, ok := recTime(rec); ok && ts.Before(cutoff) {
				continue
			}
		}
		sections := renderRecord(rec, compact)
		for _, s := range sections {
			if s.body == "" {
				continue
			}
			if !first {
				fmt.Fprintln(w)
			}
			first = false
			fmt.Fprintln(w, s.title)
			fmt.Fprintln(w, s.body)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read codex transcript: %w", err)
	}
	return nil
}

type record struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type payload struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   []contentBlock  `json:"content"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Output    json.RawMessage `json:"output"`
	Timestamp string          `json:"timestamp"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type section struct {
	title string
	body  string
}

func recTime(rec record) (time.Time, bool) {
	for _, raw := range []string{rec.Timestamp, payloadTimestamp(rec.Payload)} {
		if raw == "" {
			continue
		}
		if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return ts, true
		}
	}
	return time.Time{}, false
}

func payloadTimestamp(raw json.RawMessage) string {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return ""
	}
	return p.Timestamp
}

func renderRecord(rec record, compact bool) []section {
	if rec.Type != "response_item" && rec.Payload == nil {
		return nil
	}
	var p payload
	if err := json.Unmarshal(rec.Payload, &p); err != nil {
		return nil
	}
	switch p.Type {
	case "message":
		var out []section
		for _, b := range p.Content {
			if b.Text == "" {
				continue
			}
			switch b.Type {
			case "input_text":
				out = append(out, section{"─── User ───", b.Text})
			case "output_text":
				out = append(out, section{"─── Assistant ───", b.Text})
			case "thinking":
				if !compact {
					out = append(out, section{"─── Thinking ───", b.Text})
				}
			default:
				if p.Role == "user" {
					out = append(out, section{"─── User ───", b.Text})
				} else if p.Role == "assistant" {
					out = append(out, section{"─── Assistant ───", b.Text})
				}
			}
		}
		return out
	case "function_call":
		return []section{{fmt.Sprintf("─── Tool: %s ───", p.Name), compactJSON(p.Arguments)}}
	case "function_call_output":
		if compact {
			return nil
		}
		return []section{{"─── Result ───", compactJSON(p.Output)}}
	}
	return nil
}

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}
