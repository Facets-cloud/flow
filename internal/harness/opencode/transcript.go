package opencode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// ExportRunner runs `opencode session export <id>` and returns stdout.
// Tests replace it with a fixture.
var ExportRunner = runExport

func runExport(sessionID string) ([]byte, error) {
	cmd := exec.Command("opencode", "session", "export", sessionID)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return nil, fmt.Errorf("%w: %s", err, s)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// RenderTranscript exports the opencode session and renders it into the
// same human-readable shape used by the other harnesses.
func (o *opencode) RenderTranscript(_ string, sessionID string, compact bool, w io.Writer) error {
	data, err := ExportRunner(sessionID)
	if err != nil {
		return fmt.Errorf("opencode session export %s: %w", sessionID, err)
	}
	return RenderExport(data, compact, w)
}

// exportDoc is the subset of `opencode session export` output we render.
type exportDoc struct {
	Info     json.RawMessage `json:"info"`
	Messages []exportMessage `json:"messages"`
}

type exportMessage struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Content []exportContent `json:"content"`
}

type exportContent struct {
	Type  string           `json:"type"`
	Text  string           `json:"text"`
	Name  string           `json:"name"`
	State *exportToolState `json:"state"`
}

type exportToolState struct {
	Status  string          `json:"status"`
	Input   json.RawMessage `json:"input"`
	Content []exportContent `json:"content"`
	Error   json.RawMessage `json:"error"`
}

// RenderExport renders an `opencode session export` document. Session
// metadata, idle/system/compaction events, and unknown message types carry
// no conversational context and are skipped.
func RenderExport(data []byte, compact bool, w io.Writer) error {
	var doc exportDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("decode opencode session export: %w", err)
	}

	r := &exportRenderer{w: w, compact: compact, first: true}
	for _, msg := range doc.Messages {
		switch msg.Type {
		case "user":
			r.block("─── User ───", userText(msg.Text))
		case "assistant":
			for _, item := range msg.Content {
				r.assistantItem(item)
			}
		case "synthetic":
			if msg.Text == "" {
				continue
			}
			r.block("─── Synthetic ───", msg.Text)
		}
	}
	return nil
}

type exportRenderer struct {
	w       io.Writer
	compact bool
	first   bool
}

// block writes a label followed by body, separated from the previous block
// by exactly one blank line (none before the first).
func (r *exportRenderer) block(label, body string) {
	if !r.first {
		fmt.Fprintln(r.w)
	}
	fmt.Fprintln(r.w, label)
	fmt.Fprintln(r.w, body)
	r.first = false
}

func (r *exportRenderer) assistantItem(item exportContent) {
	switch item.Type {
	case "text":
		if item.Text == "" {
			return
		}
		r.block("─── Assistant ───", item.Text)
	case "reasoning":
		if r.compact || item.Text == "" {
			return
		}
		r.block("─── Thinking ───", truncate(item.Text, 500))
	case "tool":
		r.tool(item)
	}
}

func (r *exportRenderer) tool(item exportContent) {
	var input json.RawMessage
	if item.State != nil {
		input = item.State.Input
	}
	if !r.first {
		fmt.Fprintln(r.w)
	}
	fmt.Fprintf(r.w, "─── Tool: %s ───\n", item.Name)
	fmt.Fprintln(r.w, formatJSON(input))
	r.first = false

	if r.compact || item.State == nil {
		return
	}
	var parts []string
	for _, c := range item.State.Content {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	out := strings.Join(parts, "")
	if out == "" {
		if item.State.Status != "error" || len(item.State.Error) == 0 {
			return
		}
		out = formatJSON(item.State.Error)
	}
	r.block("─── Result ───", truncate(out, 500))
}

// userText unwraps the literal double quotes `opencode run` wraps prompts
// in. A text that is not wrapped is returned untouched.
func userText(s string) string {
	if len(s) < 2 || !strings.HasPrefix(s, `"`) || !strings.HasSuffix(s, `"`) {
		return s
	}
	if unquoted, err := strconv.Unquote(s); err == nil {
		return unquoted
	}
	return s[1 : len(s)-1]
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
