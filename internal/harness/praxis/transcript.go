package praxis

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// praxisEntry mirrors the JSON structure of a single line in praxis's
// session.jsonl. The "type" field discriminates between the session
// header ("session") and message entries ("message"). Message entries
// carry an embedded "message" object with role/content.
type praxisEntry struct {
	Type    string          `json:"type"`
	Message json.RawMessage `json:"message,omitempty"`
	// For "message" type entries, the message field is an ai.Message
	// with Role and Content. We decode it lazily.
}

// praxisMessage is the embedded message object.
type praxisMessage struct {
	Role    string              `json:"role"`
	Text    string              `json:"text"`
	Content []praxisContentPart `json:"content"`
}

// praxisContentPart represents one part of a message's content array
// (or a string, normalized to a single text part).
type praxisContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// renderJSONL reads praxis's session.jsonl byte-stream and writes a
// normalized human-readable rendering to w. Each line is a JSON object;
// "message" entries are rendered as "Role: text", other entry types
// (session headers, summaries, compaction markers) are skipped unless
// compact mode is off, in which case a light marker is emitted.
//
// compact=true omits tool results and thinking blocks (mirroring the
// claude adapter's RenderJSONL contract).
func renderJSONL(r io.Reader, compact bool, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	// Praxis transcripts can have long lines (full file contents in
	// tool results); allow up to 1MB per line.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

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

		// User messages are persisted as {role, text}; assistant messages
		// use {role, content:[{type:"text", text:"…"}]}. Normalize both.
		textParts := make([]string, 0, len(msg.Content)+1)
		if msg.Text != "" {
			textParts = append(textParts, msg.Text)
		}
		for _, part := range msg.Content {
			if part.Type == "text" && part.Text != "" {
				textParts = append(textParts, part.Text)
			}
			// compact mode skips tool_use / tool_result / thinking parts.
		}
		text := strings.Join(textParts, "\n")
		if text == "" {
			continue
		}

		role := msg.Role
		if role == "" {
			role = "unknown"
		}
		fmt.Fprintf(w, "%s: %s\n\n", role, text)
	}
	return scanner.Err()
}
