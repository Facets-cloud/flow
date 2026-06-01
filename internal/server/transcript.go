package server

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"flow/internal/agents"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type jsonlRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type jsonlMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// sessionJSONLPath resolves a task's session transcript file path.
//
// Hot-path fast lane: if tasks.session_path is populated and the file
// still exists, that path is returned without computing or walking
// anything. The UI tick hits this path many times per second per
// connected client — keeping it to a single os.Stat is the difference
// between idle and a saturated CPU when ~/.codex/sessions has grown to
// thousands of files.
//
// Fallbacks (when session_path is unset or stale):
//   - Codex: walks ~/.codex/sessions to find the file matching session_id.
//   - Claude: computes the deterministic ~/.claude/projects/<cwd>/<id>.jsonl.
//
// Self-heal: on a successful fallback resolution, the discovered path is
// written back to tasks.session_path so subsequent ticks take the fast
// lane. Persistence is best-effort — a failed UPDATE never breaks the
// caller. Pass db=nil to skip persistence (used by call sites that
// don't have a DB handle wired through).
func sessionJSONLPath(db *sql.DB, task *flowdb.Task) (string, error) {
	if !task.SessionID.Valid || task.SessionID.String == "" {
		return "", errors.New("task has no session")
	}
	if task.SessionPath.Valid && task.SessionPath.String != "" {
		if _, err := os.Stat(task.SessionPath.String); err == nil {
			return task.SessionPath.String, nil
		}
		// Stale path (file moved/archived/deleted) — fall through and
		// re-resolve. The new path will be persisted below, replacing
		// the stale value.
	}
	resolved, err := resolveSessionJSONLPath(task)
	if err != nil {
		return "", err
	}
	if db != nil && resolved != "" && resolved != task.SessionPath.String {
		// Self-heal: cache the resolved path so the next tick takes the
		// fast lane. Best-effort: a write failure is silently ignored;
		// the worst case is one extra walk on the next tick.
		_, _ = db.Exec(
			`UPDATE tasks SET session_path = ? WHERE slug = ?`,
			resolved, task.Slug,
		)
	}
	return resolved, nil
}

// backfillSessionPaths populates tasks.session_path for any Codex task
// whose session has been captured but predates the column existing.
// Without this, the first UI tick after upgrading flow would still pay
// the recursive walk cost; with it, the steady-state fast lane kicks in
// immediately on server start.
//
// Best-effort: a row-level failure logs to stderr and continues with the
// next task. The function returns nil even on partial failure so a
// pathological ~/.codex/sessions tree (e.g. unreadable) can't block
// server startup.
func (s *Server) backfillSessionPaths() {
	if s.cfg.DB == nil {
		return
	}
	rows, err := s.cfg.DB.Query(
		`SELECT slug, session_id FROM tasks
		 WHERE session_provider = 'codex'
		   AND session_id IS NOT NULL AND session_id != ''
		   AND (session_path IS NULL OR session_path = '')
		   AND deleted_at IS NULL`,
	)
	if err != nil {
		return
	}
	type pending struct{ slug, sid string }
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.slug, &p.sid); err != nil {
			continue
		}
		todo = append(todo, p)
	}
	rows.Close()
	for _, p := range todo {
		path, err := agents.FindCodexSessionPathByID(p.sid)
		if err != nil || path == "" {
			continue
		}
		_, _ = s.cfg.DB.Exec(
			`UPDATE tasks SET session_path = ? WHERE slug = ? AND session_id = ?`,
			path, p.slug, p.sid,
		)
	}
}

func resolveSessionJSONLPath(task *flowdb.Task) (string, error) {
	if task.SessionProvider == agents.ProviderCodex {
		path, err := agents.FindCodexSessionPathByID(task.SessionID.String)
		if err != nil {
			return "", fmt.Errorf("codex session file not found for %s: %w", task.SessionID.String, err)
		}
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home dir: %w", err)
	}
	// Claude derives its project dir from the cwd it was launched in. When the
	// task runs in a git worktree (flow do --worktree), that cwd is the worktree
	// path, NOT work_dir — so the jsonl lives under the worktree-encoded dir.
	// Try the worktree first, then fall back to work_dir. (Missing this is why a
	// worktree session's token count fell through to the 1.2k estimate floor:
	// the usage parse couldn't find the transcript.)
	candidates := make([]string, 0, 2)
	if wt := strings.TrimSpace(task.WorktreePath.String); task.WorktreePath.Valid && wt != "" {
		candidates = append(candidates, wt)
	}
	if wd := strings.TrimSpace(task.WorkDir); wd != "" {
		candidates = append(candidates, wd)
	}
	var lastPath string
	for _, cwd := range candidates {
		path := filepath.Join(home, ".claude", "projects", encodeCwdForClaude(cwd), task.SessionID.String+".jsonl")
		lastPath = path
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("session file not found: %s", lastPath)
}

func encodeCwdForClaude(cwd string) string {
	return strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace(cwd)
}

func parseTranscriptFile(path string) ([]TranscriptEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var entries []TranscriptEntry
	var offset int64
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		lineOffset := offset
		offset += int64(len(line)) + 1
		if len(line) == 0 {
			continue
		}
		parsed := parseTranscriptLine(line, lineOffset)
		entries = append(entries, parsed...)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

type transcriptUsageStats struct {
	// TokensUsed is the CURRENT context-window occupancy — the most recent
	// turn's token total (it's overwritten, not summed). This is the "38k/258k"
	// fill bar on a session card, paired with TokensMax (the model's context
	// window).
	TokensUsed int
	TokensMax  int
	// TokensSession is the cumulative tokens CONSUMED this session — new work
	// only, EXCLUDING cache re-reads (which would double-count the whole context
	// every turn and balloon a long session into the hundreds of millions, e.g.
	// 538M). Accumulated per turn via freshTotal(). This is the header pill
	// ("tokens used this session").
	TokensSession int
	Model         string
	LastTimestamp string
}

type transcriptUsageRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
	Message   struct {
		Model string               `json:"model"`
		Usage transcriptTokenUsage `json:"usage"`
	} `json:"message"`
}

type transcriptTokenUsage struct {
	InputTokens              int `json:"input_tokens"`
	CachedInputTokens        int `json:"cached_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	ReasoningOutputTokens    int `json:"reasoning_output_tokens"`
	TotalTokens              int `json:"total_tokens"`
	ModelContextWindow       int `json:"model_context_window"`
}

func sessionTranscriptUsageStats(path string) transcriptUsageStats {
	f, err := os.Open(path)
	if err != nil {
		return transcriptUsageStats{}
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var stats transcriptUsageStats
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec transcriptUsageRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		stats.LastTimestamp = laterTimestamp(stats.LastTimestamp, rec.Timestamp)
		if m := strings.TrimSpace(rec.Message.Model); m != "" {
			stats.Model = m
		}
		if used := rec.Message.Usage.total(); used > 0 {
			stats.TokensUsed = used // context occupancy = latest turn's full total
		}
		// Session usage = cumulative NEW work, EXCLUDING cache re-reads.
		stats.TokensSession += rec.Message.Usage.freshTotal()
		if rec.Payload != nil {
			var payload struct {
				Type string `json:"type"`
				Info struct {
					LastTokenUsage     transcriptTokenUsage `json:"last_token_usage"`
					TotalTokenUsage    transcriptTokenUsage `json:"total_token_usage"`
					ModelContextWindow int                  `json:"model_context_window"`
				} `json:"info"`
			}
			if err := json.Unmarshal(rec.Payload, &payload); err == nil && payload.Type == "token_count" {
				if used := payload.Info.LastTokenUsage.total(); used > 0 {
					stats.TokensUsed = used
				} else if used := payload.Info.TotalTokenUsage.total(); used > 0 {
					stats.TokensUsed = used
				}
				if total := payload.Info.TotalTokenUsage.total(); total > 0 {
					stats.TokensSession = total // Codex: reported running total
				}
				if payload.Info.ModelContextWindow > 0 {
					stats.TokensMax = payload.Info.ModelContextWindow
				}
			}
		}
	}
	return stats
}

func (u transcriptTokenUsage) total() int {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.InputTokens +
		u.CachedInputTokens +
		u.CacheCreationInputTokens +
		u.CacheReadInputTokens +
		u.OutputTokens +
		u.ReasoningOutputTokens
}

// freshTotal counts only NEW work for a turn — it EXCLUDES cache *reads*
// (re-reading context already counted on earlier turns). Summing total() per
// turn double-counts the cached context every turn, ballooning a session into
// the hundreds of millions (e.g. 218k context × thousands of turns ≈ 538M);
// freshTotal is the accurate "tokens consumed this session" figure. Claude
// reports new input in InputTokens with cache reads separate in
// CacheReadInputTokens; Codex bundles the cached portion into InputTokens,
// exposed as CachedInputTokens, so subtract it.
func (u transcriptTokenUsage) freshTotal() int {
	in := u.InputTokens - u.CachedInputTokens
	if in < 0 {
		in = 0
	}
	return in + u.CacheCreationInputTokens + u.OutputTokens + u.ReasoningOutputTokens
}

func parseTranscriptLine(line []byte, offset int64) []TranscriptEntry {
	if entries := parseCodexTranscriptLine(line, offset); len(entries) > 0 {
		return entries
	}
	var rec jsonlRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil
	}
	switch rec.Type {
	case "user":
		return stampTranscriptEntries(parseUserRecord(rec.Message, offset), rec.Timestamp)
	case "assistant":
		return stampTranscriptEntries(parseAssistantRecord(rec.Message, offset), rec.Timestamp)
	default:
		return nil
	}
}

type codexTranscriptRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Payload   json.RawMessage `json:"payload"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	CallID    string          `json:"call_id"`
	Output    json.RawMessage `json:"output"`
	Action    struct {
		Command []string `json:"command"`
	} `json:"action"`
}

type codexTranscriptBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func parseCodexTranscriptLine(line []byte, offset int64) []TranscriptEntry {
	var rec codexTranscriptRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil
	}
	switch rec.Type {
	case "response_item":
		var payload codexTranscriptRecord
		if err := json.Unmarshal(rec.Payload, &payload); err != nil {
			return nil
		}
		if payload.Timestamp == "" {
			payload.Timestamp = rec.Timestamp
		}
		return stampTranscriptEntries(codexPayloadEntries(payload, offset), payload.Timestamp)
	case "message", "function_call", "function_call_output", "local_shell_call":
		return stampTranscriptEntries(codexPayloadEntries(rec, offset), rec.Timestamp)
	default:
		return nil
	}
}

func stampTranscriptEntries(entries []TranscriptEntry, timestamp string) []TranscriptEntry {
	if timestamp == "" {
		return entries
	}
	for i := range entries {
		if entries[i].Timestamp == "" {
			entries[i].Timestamp = timestamp
		}
	}
	return entries
}

func codexPayloadEntries(rec codexTranscriptRecord, offset int64) []TranscriptEntry {
	switch rec.Type {
	case "message":
		return codexMessageEntries(rec.Role, rec.Content, offset)
	case "function_call":
		return []TranscriptEntry{{
			Type:             "tool_use",
			ToolName:         rec.Name,
			ToolInputSummary: truncate(rec.Arguments, 220),
			ByteOffset:       offset,
		}}
	case "function_call_output":
		return []TranscriptEntry{{
			Type:           "tool_result",
			ToolResultText: truncate(rawJSONAsText(rec.Output), 500),
			ByteOffset:     offset,
		}}
	case "local_shell_call":
		return []TranscriptEntry{{
			Type:             "tool_use",
			ToolName:         "local_shell",
			ToolInputSummary: strings.Join(rec.Action.Command, " "),
			ByteOffset:       offset,
		}}
	default:
		return nil
	}
}

func codexMessageEntries(role string, raw json.RawMessage, offset int64) []TranscriptEntry {
	entryType := "assistant"
	if role == "user" {
		entryType = "user"
	}
	var blocks []codexTranscriptBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var entries []TranscriptEntry
		for _, block := range blocks {
			if block.Text == "" {
				continue
			}
			switch block.Type {
			case "input_text", "output_text", "text":
				entries = append(entries, TranscriptEntry{Type: entryType, Text: block.Text, ByteOffset: offset})
			}
		}
		return entries
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil && text != "" {
		return []TranscriptEntry{{Type: entryType, Text: text, ByteOffset: offset}}
	}
	return nil
}

func rawJSONAsText(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

type codexPendingUserInput struct {
	CallID    string
	Timestamp string
	Question  string
	RawJSON   string
	Seq       int
}

func pendingCodexUserInput(path string) (*codexPendingUserInput, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	pending := map[string]codexPendingUserInput{}
	seq := 0
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		seq++
		rec, ok := codexPayloadRecord(line)
		if !ok {
			continue
		}
		switch rec.Type {
		case "message":
			if rec.Role == "user" {
				pending = map[string]codexPendingUserInput{}
			}
		case "function_call":
			if !codexRequestUserInputTool(rec.Name) {
				continue
			}
			pending = map[string]codexPendingUserInput{}
			callID := strings.TrimSpace(rec.CallID)
			if callID == "" {
				callID = fmt.Sprintf("offset-%d", seq)
			}
			question := codexUserInputQuestion(rec.Arguments)
			if question == "" {
				question = "The Codex session is waiting for your input."
			}
			pending[callID] = codexPendingUserInput{
				CallID:    callID,
				Timestamp: rec.Timestamp,
				Question:  question,
				RawJSON:   string(line),
				Seq:       seq,
			}
		case "function_call_output":
			if callID := strings.TrimSpace(rec.CallID); callID != "" {
				delete(pending, callID)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	var latest *codexPendingUserInput
	for _, item := range pending {
		item := item
		if latest == nil || item.Seq > latest.Seq {
			latest = &item
		}
	}
	return latest, nil
}

func codexPayloadRecord(line []byte) (codexTranscriptRecord, bool) {
	var rec codexTranscriptRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return codexTranscriptRecord{}, false
	}
	if rec.Type == "response_item" {
		var payload codexTranscriptRecord
		if err := json.Unmarshal(rec.Payload, &payload); err != nil {
			return codexTranscriptRecord{}, false
		}
		if payload.Timestamp == "" {
			payload.Timestamp = rec.Timestamp
		}
		return payload, true
	}
	return rec, true
}

func codexRequestUserInputTool(name string) bool {
	tool := normalizeAgentHookPart(name)
	return tool == "request_user_input" || strings.Contains(tool, "request_user_input")
}

func codexUserInputQuestion(arguments string) string {
	var args struct {
		Questions []struct {
			Header   string `json:"header"`
			Question string `json:"question"`
		} `json:"questions"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	for i, question := range args.Questions {
		text := strings.TrimSpace(question.Question)
		if text == "" {
			continue
		}
		if remaining := len(args.Questions) - i - 1; remaining > 0 {
			return truncateText(fmt.Sprintf("%s (+%d more)", text, remaining), 220)
		}
		return truncateText(text, 220)
	}
	return ""
}

func parseUserRecord(raw json.RawMessage, offset int64) []TranscriptEntry {
	var msg jsonlMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil
	}
	var plain string
	if err := json.Unmarshal(msg.Content, &plain); err == nil {
		return []TranscriptEntry{{Type: "user", Text: plain, ByteOffset: offset}}
	}
	var blocks []contentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil
	}
	var entries []TranscriptEntry
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				entries = append(entries, TranscriptEntry{Type: "user", Text: b.Text, ByteOffset: offset})
			}
		case "tool_result":
			entries = append(entries, TranscriptEntry{
				Type:           "tool_result",
				ToolResultText: toolResultText(b),
				IsError:        b.IsError,
				ByteOffset:     offset,
			})
		}
	}
	return entries
}

func parseAssistantRecord(raw json.RawMessage, offset int64) []TranscriptEntry {
	var msg jsonlMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil
	}
	var entries []TranscriptEntry
	for _, b := range blocks {
		switch b.Type {
		case "thinking":
			if b.Thinking != "" {
				entries = append(entries, TranscriptEntry{Type: "thinking", Text: b.Thinking, ByteOffset: offset})
			}
		case "text":
			if b.Text != "" {
				entries = append(entries, TranscriptEntry{Type: "assistant", Text: b.Text, ByteOffset: offset})
			}
		case "tool_use":
			entries = append(entries, TranscriptEntry{
				Type:             "tool_use",
				ToolName:         b.Name,
				ToolInputSummary: formatToolInput(b.Name, b.Input),
				ByteOffset:       offset,
			})
		}
	}
	return entries
}

func toolResultText(b contentBlock) string {
	var text string
	if err := json.Unmarshal(b.Content, &text); err == nil {
		return text
	}
	var blocks []contentBlock
	if err := json.Unmarshal(b.Content, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

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
	case "Read", "Write", "Edit":
		if fp, ok := m["file_path"].(string); ok {
			return fp
		}
	case "Glob":
		if p, ok := m["pattern"].(string); ok {
			return p
		}
	case "Grep":
		if p, ok := m["pattern"].(string); ok {
			if path, ok := m["path"].(string); ok {
				return p + " in " + path
			}
			return p
		}
	}
	compact, err := json.Marshal(m)
	if err != nil {
		return string(raw)
	}
	return truncate(string(compact), 220)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
