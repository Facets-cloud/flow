package claude

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

// RenderTranscript opens the session jsonl at the claude convention
// path (~/.claude/projects/<encoded-cwd>/<sessionID>.jsonl), decodes
// the claude-specific JSONL schema, and writes a normalized human-
// readable rendering to w.
//
// cwd is the directory claude was started in (typically
// tasks.session_cwd; callers in app/ fall back to task.work_dir for
// legacy NULL rows). Claude keys its transcript path on its startup
// cwd; the two can diverge for `flow do --here` binds.
func (c *claude) RenderTranscript(cwd, sessionID string, compact bool, cutoff time.Time, w io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("no home dir: %w", err)
	}
	encoded := EncodeCwd(cwd)
	p := filepath.Join(home, ".claude", "projects", encoded, sessionID+".jsonl")
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf(
				"claude transcript not found at %s (cwd=%q maps to project dir %q). "+
					"the file might be under a different project dir if claude was started elsewhere",
				p, cwd, encoded,
			)
		}
		return fmt.Errorf("open claude transcript %s: %w", p, err)
	}
	defer f.Close()
	return RenderJSONL(f, compact, cutoff, w)
}

// RenderJSONL renders a claude session jsonl byte-stream to w. Exposed
// (not just used by RenderTranscript) so tests can exercise the
// decoder against fixture data in tempdir without going through path
// resolution.
//
// cutoff scopes the output to entries with timestamp >= cutoff. Pass
// the zero time.Time to disable the filter. Entries with a missing or
// unparseable `timestamp` field are kept regardless of cutoff —
// silent data loss in a KB-distill input is worse than an over-
// inclusive sweep.
func RenderJSONL(r io.Reader, compact bool, cutoff time.Time, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	// Session jsonl lines can be very long (tool results with file contents).
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	first := true
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var rec jsonlRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // skip malformed lines
		}

		// Filter: drop entries strictly before the cutoff. Defensive
		// on parse failure / missing field — keep the entry rather
		// than silently dropping it. RFC3339Nano accepts both the
		// jsonl's fractional-second UTC form and the DB's offset
		// form without fractional, so we use it as a single parser
		// for both sources.
		if !cutoff.IsZero() && rec.Timestamp != "" {
			if ts, perr := time.Parse(time.RFC3339Nano, rec.Timestamp); perr == nil && ts.Before(cutoff) {
				continue
			}
		}

		switch rec.Type {
		case "user":
			if !first {
				fmt.Fprintln(w)
			}
			first = false
			renderUserRecord(w, rec.Message, compact)
		case "assistant":
			if !first {
				fmt.Fprintln(w)
			}
			first = false
			renderAssistantRecord(w, rec.Message, compact)
		}
		// Skip permission-mode, file-history-snapshot, attachment, etc.
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read session: %w", err)
	}
	return nil
}

// ---------- jsonl record types ----------

// jsonlRecord is the top-level structure of each line in a Claude
// session jsonl.
type jsonlRecord struct {
	Type      string          `json:"type"`
	Message   json.RawMessage `json:"message"`
	Timestamp string          `json:"timestamp"`
}

// jsonlMessage is the message body inside user/assistant records.
type jsonlMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// contentBlock represents one block in the content array.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`        // tool_use: tool name
	ID        string          `json:"id"`          // tool_use: tool_use_id
	Input     json.RawMessage `json:"input"`       // tool_use: input params
	ToolUseID string          `json:"tool_use_id"` // tool_result
	Content   json.RawMessage `json:"content"`     // tool_result: content (string or array)
	IsError   bool            `json:"is_error"`    // tool_result
}

// ---------- rendering ----------

const maxToolResultLen = 500

func renderUserRecord(w io.Writer, raw json.RawMessage, compact bool) {
	var msg jsonlMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}

	// Content can be a plain string (user message) or an array (tool results).
	var plainText string
	if err := json.Unmarshal(msg.Content, &plainText); err == nil {
		fmt.Fprintln(w, "─── User ───")
		fmt.Fprintln(w, plainText)
		return
	}

	var blocks []contentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return
	}

	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			if compact {
				continue
			}
			renderToolResult(w, b)
		case "text":
			if b.Text != "" {
				fmt.Fprintln(w, "─── User ───")
				fmt.Fprintln(w, b.Text)
			}
		}
	}
}

func renderAssistantRecord(w io.Writer, raw json.RawMessage, compact bool) {
	var msg jsonlMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}

	var blocks []contentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return
	}

	for _, b := range blocks {
		switch b.Type {
		case "thinking":
			if compact {
				continue
			}
			if b.Thinking != "" {
				fmt.Fprintln(w, "─── Thinking ───")
				fmt.Fprintln(w, b.Thinking)
			}
		case "text":
			if b.Text != "" {
				fmt.Fprintln(w, "─── Assistant ───")
				fmt.Fprintln(w, b.Text)
			}
		case "tool_use":
			renderToolUse(w, b)
		}
	}
}

func renderToolUse(w io.Writer, b contentBlock) {
	summary := formatToolInput(b.Name, b.Input)
	fmt.Fprintf(w, "─── Tool: %s ───\n", b.Name)
	fmt.Fprintln(w, summary)
}

func renderToolResult(w io.Writer, b contentBlock) {
	// Content can be a string or an array of content blocks.
	var text string
	if err := json.Unmarshal(b.Content, &text); err == nil {
		label := "─── Result ───"
		if b.IsError {
			label = "─── Result (error) ───"
		}
		fmt.Fprintln(w, label)
		fmt.Fprintln(w, truncate(text, maxToolResultLen))
		return
	}

	// Array form: extract text blocks.
	var inner []contentBlock
	if err := json.Unmarshal(b.Content, &inner); err != nil {
		return
	}
	for _, ib := range inner {
		if ib.Type == "text" && ib.Text != "" {
			label := "─── Result ───"
			if b.IsError {
				label = "─── Result (error) ───"
			}
			fmt.Fprintln(w, label)
			fmt.Fprintln(w, truncate(ib.Text, maxToolResultLen))
		}
	}
}

// formatToolInput returns a compact one-line summary of a tool call's input.
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
	case "Read":
		if fp, ok := m["file_path"].(string); ok {
			parts := []string{fp}
			if off, ok := m["offset"].(float64); ok {
				parts = append(parts, fmt.Sprintf("offset=%d", int(off)))
			}
			if lim, ok := m["limit"].(float64); ok {
				parts = append(parts, fmt.Sprintf("limit=%d", int(lim)))
			}
			return strings.Join(parts, " ")
		}
	case "Write":
		if fp, ok := m["file_path"].(string); ok {
			return fp
		}
	case "Edit":
		if fp, ok := m["file_path"].(string); ok {
			return fp
		}
	case "Glob":
		if p, ok := m["pattern"].(string); ok {
			return p
		}
	case "Grep":
		if p, ok := m["pattern"].(string); ok {
			parts := []string{p}
			if path, ok := m["path"].(string); ok {
				parts = append(parts, "in "+path)
			}
			return strings.Join(parts, " ")
		}
	case "Agent":
		if desc, ok := m["description"].(string); ok {
			return desc
		}
		if prompt, ok := m["prompt"].(string); ok {
			return truncate(prompt, 120)
		}
	}

	// Fallback: compact JSON of the input.
	compact, err := json.Marshal(m)
	if err != nil {
		return string(raw)
	}
	return truncate(string(compact), 200)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
