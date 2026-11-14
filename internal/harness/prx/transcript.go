package prx

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// transcriptPath returns the session's transcript: session.jsonl, or
// session.jsonl.gz once prx has compressed an idle session.
func transcriptPath(sessionID string) (string, error) {
	dir, err := sessionDir()
	if err != nil {
		return "", err
	}
	base := filepath.Join(dir, sessionID, "session.jsonl")
	for _, p := range []string{base, base + ".gz"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("prx transcript not found: no %s[.gz]", base)
}

// RenderTranscript reads the id-keyed transcript; cwd is not part of the
// path, so it's ignored.
func (p *prx) RenderTranscript(_ string, sessionID string, compact bool, w io.Writer) error {
	path, err := transcriptPath(sessionID)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open prx transcript %s: %w", path, err)
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("decompress prx transcript %s: %w", path, err)
		}
		defer gz.Close()
		r = gz
	}
	return RenderJSONL(r, compact, w)
}

type message struct {
	Role     string         `json:"role"`
	Text     string         `json:"text"`
	Content  []contentBlock `json:"content"`
	ToolName string         `json:"toolName"`
	IsError  bool           `json:"isError"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// RenderJSONL renders prx's `message` entries in the section shape the other
// adapters write. Header, model changes, compaction and other bookkeeping
// entries are skipped. compact drops thinking and tool results.
func RenderJSONL(r io.Reader, compact bool, w io.Writer) error {
	sw := &sectionWriter{w: w}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		var entry struct {
			Type    string  `json:"type"`
			Message message `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Type != "message" {
			continue
		}
		renderMessage(sw, entry.Message, compact)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read prx transcript: %w", err)
	}
	return nil
}

func renderMessage(sw *sectionWriter, m message, compact bool) {
	switch m.Role {
	case "user":
		if m.Text != "" {
			sw.section("─── User ───", m.Text)
		}
		for _, b := range m.Content {
			if b.Type == "text" && b.Text != "" {
				sw.section("─── User ───", b.Text)
			}
		}
	case "assistant":
		for _, b := range m.Content {
			renderAssistantBlock(sw, b, compact)
		}
	case "toolResult":
		if compact {
			return
		}
		label := "─── Result ───"
		if m.IsError {
			label = "─── Result (error) ───"
		}
		var parts []string
		for _, b := range m.Content {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		sw.section(label, truncate(strings.Join(parts, "\n"), 500))
	}
}

func renderAssistantBlock(sw *sectionWriter, b contentBlock, compact bool) {
	switch b.Type {
	case "text":
		if b.Text != "" {
			sw.section("─── Assistant ───", b.Text)
		}
	case "thinking":
		if !compact && b.Text != "" {
			sw.section("─── Thinking ───", b.Text)
		}
	case "toolCall":
		sw.section(fmt.Sprintf("─── Tool: %s ───", b.Name), string(b.Arguments))
	}
}

// sectionWriter prints labeled sections separated by one blank line.
type sectionWriter struct {
	w       io.Writer
	written bool
}

func (s *sectionWriter) section(label, body string) {
	if s.written {
		fmt.Fprintln(s.w)
	}
	fmt.Fprintln(s.w, label)
	fmt.Fprintln(s.w, body)
	s.written = true
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
