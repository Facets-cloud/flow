package app

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cmdTranscript implements `flow transcript <task-slug>`. It reads the
// task's Claude session jsonl and outputs a human-readable conversation
// transcript. This enables cross-task context sharing: one task's
// execution session can pipe the output into its context to learn what
// happened in a sibling task's conversation.
func cmdTranscript(args []string) int {
	// Positional arg first, then flags (same pattern as cmdDo).
	ref := ""
	flagArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		ref = args[0]
		flagArgs = args[1:]
	}

	fs := flagSet("transcript")
	compact := fs.Bool("compact", false, "omit tool results and thinking blocks")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open db: %v\n", err)
		return 1
	}
	defer db.Close()

	var task *flowdb.Task
	if ref == "" {
		bound, lookupErr := currentSessionTask(db)
		if lookupErr != nil {
			if isNoBindingErr(lookupErr) {
				if currentSessionID() == "" {
					fmt.Fprintln(os.Stderr, "error: no task ref given and not running inside a Claude session ($CLAUDE_CODE_SESSION_ID unset)")
				} else {
					fmt.Fprintln(os.Stderr, "error: no task ref given and this Claude session is not bound to a task")
				}
				return 2
			}
			fmt.Fprintf(os.Stderr, "error: lookup task by session: %v\n", lookupErr)
			return 1
		}
		task = bound
	} else {
		task, err = resolveTaskRef(db, ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
	}

	if !task.SessionID.Valid || task.SessionID.String == "" {
		fmt.Fprintf(os.Stderr, "error: task %q has no session — run `flow do %s` first\n", task.Slug, task.Slug)
		return 1
	}

	jsonlPath, err := sessionJSONLPath(task)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Compute the cutoff from session_started so the transcript output is
	// scoped to the task's own conversation, not pre-bind dispatch chatter
	// that --here-bound tasks accumulate. NULL/unparseable session_started
	// → zero cutoff → filter is a no-op (pass everything through).
	var cutoff time.Time
	if task.SessionStarted.Valid && task.SessionStarted.String != "" {
		if ts, perr := time.Parse(time.RFC3339Nano, task.SessionStarted.String); perr == nil {
			cutoff = ts
		}
	}

	return renderTranscript(jsonlPath, *compact, cutoff)
}

// sessionJSONLPath returns the absolute path to a task's session jsonl file.
func sessionJSONLPath(task *flowdb.Task) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	encoded := EncodeCwdForClaude(task.WorkDir)
	p := filepath.Join(home, ".claude", "projects", encoded, task.SessionID.String+".jsonl")
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("session file not found: %s", p)
	}
	return p, nil
}

// ---------- jsonl record types ----------

// jsonlRecord is the top-level structure of each line in a Claude session jsonl.
//
// Timestamp is parsed when present so the close-out sweep (and any caller
// passing a cutoff) can scope the transcript to entries on-or-after a
// specific moment — needed because --here-bound tasks carry pre-bind
// dispatch chatter in their jsonl, which would otherwise leak into KB
// distillation.
type jsonlRecord struct {
	Type      string          `json:"type"`
	Message   json.RawMessage `json:"message"`
	Timestamp string          `json:"timestamp"`
}

// jsonlMessage is the message body inside user/assistant records.
type jsonlMessage struct {
	Role    string            `json:"role"`
	Content json.RawMessage   `json:"content"`
}

// contentBlock represents one block in the content array.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`      // tool_use: tool name
	ID        string          `json:"id"`        // tool_use: tool_use_id
	Input     json.RawMessage `json:"input"`     // tool_use: input params
	ToolUseID string          `json:"tool_use_id"` // tool_result
	Content   json.RawMessage `json:"content"`     // tool_result: content (string or array)
	IsError   bool            `json:"is_error"`    // tool_result
}

// ---------- rendering ----------

const maxToolResultLen = 500

// renderTranscript reads a jsonl file and prints a human-readable transcript.
//
// cutoff scopes the output to entries with timestamp >= cutoff. Pass the
// zero time.Time to disable the filter. Entries with a missing or
// unparseable `timestamp` field are kept regardless of cutoff — silent
// data loss in a KB-distill input is worse than an over-inclusive sweep.
func renderTranscript(path string, compact bool, cutoff time.Time) int {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
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

		// Filter: drop entries strictly before the cutoff. Defensive on
		// parse failure / missing field — keep the entry rather than
		// silently dropping it. RFC3339Nano accepts both the jsonl's
		// fractional-second UTC form ("...T10:00:00.000Z") and the DB's
		// offset form ("...+05:30") without fractional, so we use it as
		// a single parser for both sources.
		if !cutoff.IsZero() && rec.Timestamp != "" {
			if ts, perr := time.Parse(time.RFC3339Nano, rec.Timestamp); perr == nil && ts.Before(cutoff) {
				continue
			}
		}

		switch rec.Type {
		case "user":
			if !first {
				fmt.Println()
			}
			first = false
			renderUserRecord(rec.Message, compact)
		case "assistant":
			if !first {
				fmt.Println()
			}
			first = false
			renderAssistantRecord(rec.Message, compact)
		}
		// Skip permission-mode, file-history-snapshot, attachment, etc.
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "error reading session file: %v\n", err)
		return 1
	}
	return 0
}

func renderUserRecord(raw json.RawMessage, compact bool) {
	var msg jsonlMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}

	// Content can be a plain string (user message) or an array (tool results).
	var plainText string
	if err := json.Unmarshal(msg.Content, &plainText); err == nil {
		fmt.Println("─── User ───")
		fmt.Println(plainText)
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
			renderToolResult(b)
		case "text":
			if b.Text != "" {
				fmt.Println("─── User ───")
				fmt.Println(b.Text)
			}
		}
	}
}

func renderAssistantRecord(raw json.RawMessage, compact bool) {
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
				fmt.Println("─── Thinking ───")
				fmt.Println(b.Thinking)
			}
		case "text":
			if b.Text != "" {
				fmt.Println("─── Assistant ───")
				fmt.Println(b.Text)
			}
		case "tool_use":
			renderToolUse(b)
		}
	}
}

func renderToolUse(b contentBlock) {
	summary := formatToolInput(b.Name, b.Input)
	fmt.Printf("─── Tool: %s ───\n", b.Name)
	fmt.Println(summary)
}

func renderToolResult(b contentBlock) {
	// Content can be a string or an array of content blocks.
	var text string
	if err := json.Unmarshal(b.Content, &text); err == nil {
		label := "─── Result ───"
		if b.IsError {
			label = "─── Result (error) ───"
		}
		fmt.Println(label)
		fmt.Println(truncate(text, maxToolResultLen))
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
			fmt.Println(label)
			fmt.Println(truncate(ib.Text, maxToolResultLen))
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

// SessionJSONLPathForTask is the exported wrapper for use by other packages
// or tests. Returns ("", error) if the task has no session or the file is
// missing.
func SessionJSONLPathForTask(db *sql.DB, ref string) (string, error) {
	task, err := resolveTaskRef(db, ref)
	if err != nil {
		return "", err
	}
	if !task.SessionID.Valid || task.SessionID.String == "" {
		return "", errors.New("task has no session")
	}
	return sessionJSONLPath(task)
}
