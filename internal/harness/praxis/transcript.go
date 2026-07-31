package praxis

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// praxisEntry mirrors the JSON structure of a single line in praxis's
// session.jsonl. The "type" field discriminates between the session
// header ("session"), message entries ("message"), and bookkeeping
// entries (e.g. "todo_update") that carry no transcript content.
type praxisEntry struct {
	Type    string          `json:"type"`
	Message json.RawMessage `json:"message,omitempty"`
}

// praxisMessage is the embedded message object (an ai.Message). Role is
// one of user / assistant / toolResult. User turns carry their prompt in
// "text"; assistant turns and tool results carry a "content" array.
type praxisMessage struct {
	Role     string          `json:"role"`
	Text     string          `json:"text"`
	Content  json.RawMessage `json:"content"`
	ToolName string          `json:"toolName"`
}

// praxisContentPart is one element of a message's content array. Praxis
// part types are "text", "thinking", and "toolCall" (note the camelCase
// — they are not Anthropic wire-format names).
type praxisContentPart struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`      // toolCall: tool name
	Arguments json.RawMessage `json:"arguments"` // toolCall: tool input
}

const (
	// maxToolResultLen matches the claude adapter so both harnesses
	// render tool output at the same fidelity.
	maxToolResultLen = 500
	// maxToolArgsLen bounds the one-line tool-call summary. Praxis tool
	// arguments can embed whole file bodies (write.content, eval.code),
	// so the fallback rendering must not be unbounded.
	maxToolArgsLen = 300
)

// renderJSONL reads praxis's session.jsonl byte-stream and writes a
// human-readable rendering to w, using the same section vocabulary as
// the claude adapter ("─── User ───", "─── Assistant ───",
// "─── Thinking ───", "─── Tool: x ───", "─── Result ───") so callers
// see one normalized transcript shape regardless of harness.
//
// compact=true omits thinking blocks and tool results — the bulk of a
// long session — while keeping user text, assistant text, and the
// one-line record of which tools ran. Non-message entries (session
// header, todo updates) are always skipped.
func renderJSONL(r io.Reader, compact bool, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	// Praxis transcripts can have long lines (full file contents in tool
	// results); match the claude adapter's 10MB ceiling.
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	out := &sectionWriter{w: w}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry praxisEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			// Skip unparseable lines rather than failing the whole render.
			continue
		}
		if entry.Type != "message" || len(entry.Message) == 0 {
			continue
		}
		var msg praxisMessage
		if err := json.Unmarshal(entry.Message, &msg); err != nil {
			continue
		}

		out.startRecord()
		switch msg.Role {
		case "user":
			renderUserMessage(out, msg)
		case "assistant":
			renderAssistantMessage(out, msg, compact)
		case "toolResult":
			if compact {
				continue
			}
			renderToolResult(out, msg)
		}
		// Any other role (e.g. "system") carries no user-facing turn.
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read session: %w", err)
	}
	return nil
}

func renderUserMessage(out *sectionWriter, msg praxisMessage) {
	if msg.Text != "" {
		out.section("─── User ───", msg.Text)
	}
	// A user turn normally persists its prompt in "text", but tolerate
	// the content-array form too.
	for _, part := range decodeParts(msg.Content) {
		if part.Type == "text" && part.Text != "" {
			out.section("─── User ───", part.Text)
		}
	}
}

func renderAssistantMessage(out *sectionWriter, msg praxisMessage, compact bool) {
	for _, part := range decodeParts(msg.Content) {
		switch part.Type {
		case "text":
			if part.Text != "" {
				out.section("─── Assistant ───", part.Text)
			}
		case "thinking":
			if compact {
				continue
			}
			if part.Text != "" {
				out.section("─── Thinking ───", part.Text)
			}
		case "toolCall":
			name := part.Name
			if name == "" {
				name = "unknown"
			}
			out.section("─── Tool: "+name+" ───", formatToolArgs(part.Arguments))
		}
	}
}

func renderToolResult(out *sectionWriter, msg praxisMessage) {
	// The tool name lives on the toolCall part; the result message's
	// toolName field is frequently null, so only label with it when set.
	label := "─── Result ───"
	if msg.ToolName != "" {
		label = "─── Result: " + msg.ToolName + " ───"
	}
	for _, part := range decodeParts(msg.Content) {
		if part.Type == "text" && part.Text != "" {
			out.section(label, truncate(part.Text, maxToolResultLen))
		}
	}
}

// decodeParts normalizes a message's content field, which is either an
// array of parts or a bare string, into a part slice. An absent or
// undecodable content yields no parts.
func decodeParts(raw json.RawMessage) []praxisContentPart {
	if len(raw) == 0 {
		return nil
	}
	var parts []praxisContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		return parts
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil && text != "" {
		return []praxisContentPart{{Type: "text", Text: text}}
	}
	return nil
}

// formatToolArgs returns a one-line summary of a tool call's arguments.
// Praxis tool names are lowercase and vary by build, so this keys off
// the argument names — which are stable across the tool set — rather
// than off a tool-name table that would drift from the harness's
// registry.
func formatToolArgs(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return truncate(string(raw), maxToolArgsLen)
	}
	if cmd, ok := m["command"].(string); ok && cmd != "" {
		return "$ " + truncate(cmd, maxToolArgsLen)
	}
	// Ordered on informativeness: grep/ast_edit carry both pattern and
	// path, and the pattern is what identifies the call.
	for _, key := range []string{"pattern", "path", "file_path", "query", "question", "goal", "op"} {
		if v, ok := m[key].(string); ok && v != "" {
			return truncate(v, maxToolArgsLen)
		}
	}
	return truncate(string(raw), maxToolArgsLen)
}

// truncate cuts s to at most max bytes without splitting a UTF-8 rune,
// appending an ellipsis when it shortens.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// sectionWriter writes labelled sections and separates *records* (not
// sections within a record) with a blank line, matching the claude
// adapter's layout. The separator is emitted lazily on the record's
// first section so records that render to nothing — a tool result in
// compact mode, an empty assistant turn — leave no stray gap.
type sectionWriter struct {
	w       io.Writer
	wrote   bool
	pending bool
}

func (s *sectionWriter) startRecord() { s.pending = true }

func (s *sectionWriter) section(label, body string) {
	if s.pending && s.wrote {
		fmt.Fprintln(s.w)
	}
	s.pending = false
	s.wrote = true
	fmt.Fprintln(s.w, label)
	fmt.Fprintln(s.w, body)
}
