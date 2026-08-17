package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// RenderTranscript finds Codex's sid-keyed rollout JSONL and renders its
// response items into the same human-readable shape used by other harnesses.
func (c *codex) RenderTranscript(_ string, sessionID string, compact bool, w io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("no home dir: %w", err)
	}
	p, err := resolveTranscriptPath(filepath.Join(home, ".codex", "sessions"), sessionID)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return fmt.Errorf("open Codex transcript %s: %w", p, err)
	}
	defer f.Close()
	return RenderJSONL(f, compact, w)
}

func resolveTranscriptPath(root, sessionID string) (string, error) {
	var matches []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() && strings.HasSuffix(path, "-"+sessionID+".jsonl") {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("scan Codex sessions under %s: %w", root, err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("Codex transcript not found: no rollout ending in %s under %s", sessionID, root)
	}
	newest := matches[0]
	newestMod := int64(-1)
	for _, p := range matches {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().UnixNano() > newestMod {
			newest, newestMod = p, fi.ModTime().UnixNano()
		}
	}
	return newest, nil
}

// RenderJSONL renders persisted Codex response_item records. Session metadata,
// turn context, and provider events are intentionally omitted: they do not add
// conversational context for `flow transcript` or the close-out sweep.
func RenderJSONL(r io.Reader, compact bool, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	first := true
	for scanner.Scan() {
		var rec struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil || rec.Type != "response_item" {
			continue
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
			continue
		}
		var rendered bool
		switch item.Type {
		case "message":
			if item.Role == "developer" {
				continue
			}
			label := "─── User ───"
			if item.Role == "assistant" {
				label = "─── Assistant ───"
			}
			for _, block := range item.Content {
				if block.Text == "" {
					continue
				}
				if !first && !rendered {
					fmt.Fprintln(w)
				}
				fmt.Fprintln(w, label)
				fmt.Fprintln(w, block.Text)
				rendered = true
			}
		case "custom_tool_call":
			if !first {
				fmt.Fprintln(w)
			}
			fmt.Fprintf(w, "─── Tool: %s ───\n", item.Name)
			fmt.Fprintln(w, formatJSON(item.Input))
			rendered = true
		case "custom_tool_call_output":
			if compact {
				continue
			}
			if !first {
				fmt.Fprintln(w)
			}
			fmt.Fprintln(w, "─── Result ───")
			fmt.Fprintln(w, truncate(formatJSON(item.Output), 500))
			rendered = true
		}
		if rendered {
			first = false
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read Codex session: %w", err)
	}
	return nil
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func formatJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		if pretty, err := json.Marshal(v); err == nil {
			return string(pretty)
		}
	}
	return string(raw)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
