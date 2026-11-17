package harness

import "io"

// Record roles in a normalized flow transcript copy. The set is fixed and
// harness-agnostic: every adapter maps its own schema onto these.
const (
	RoleUser       = "user"
	RoleAssistant  = "assistant"
	RoleToolCall   = "tool_call"
	RoleToolResult = "tool_result"
	RoleSystem     = "system"
)

// NormalizedRecord is one line of a flow-owned transcript copy
// (~/.flow/tasks/<slug>/transcripts/<harness>-<session-id>.jsonl). It is
// the SAME shape for every harness, so recall and `flow transcript` never
// decode harness-specific bytes once a copy exists.
//
// The on-disk JSONL field names are the documented schema; keep them
// stable (docs/transcripts.md). Harness normalizers fill Role/Text/Tool/
// Injected and TS when the source line carries a timestamp; the capture
// layer stamps Harness, SessionID, Turn and a fallback TS.
type NormalizedRecord struct {
	TS        string `json:"ts"`                  // RFC3339; capture fills now if the source has none
	Harness   string `json:"harness"`             // claude|codex|prx
	SessionID string `json:"session_id"`          // the harness session id this copy belongs to
	Turn      int    `json:"turn"`                // 1-based user-prompt counter at the time of the record
	Role      string `json:"role"`                // user|assistant|tool_call|tool_result|system
	Text      string `json:"text,omitempty"`      // message text, tool input, or tool output
	Tool      string `json:"tool,omitempty"`      // tool name (tool_call / tool_result)
	Injected  bool   `json:"injected,omitempty"`  // hook/skill/system-reminder payload — flagged, not dropped
}

// TranscriptNormalizer is an OPTIONAL harness capability: decode ONE raw
// transcript line into zero or more NormalizedRecords. It is deliberately
// line-scoped so the capture layer can read incrementally from a saved
// byte offset and hand over exactly the complete lines that have arrived.
//
// Implementations must be total and side-effect free: a line they do not
// recognise (session metadata, bookkeeping, a malformed fragment) returns
// (nil, nil), never an error that would abort a capture. They fill Role,
// Text, Tool, Injected and — when the line carries one — TS; everything
// else is the capture layer's job.
type TranscriptNormalizer interface {
	NormalizeLine(line []byte) ([]NormalizedRecord, error)
}

// StreamNormalizer is an OPTIONAL harness capability for sources that are
// not line-delimited the way the offset reader expects (e.g. a compressed
// or whole-file transcript). Given a reader over the raw bytes it returns
// every record. Adapters that implement TranscriptNormalizer usually also
// implement this by scanning lines; the capture layer prefers the
// incremental TranscriptNormalizer and falls back to StreamNormalizer for
// whole-file reads (backfill, compressed sources).
type StreamNormalizer interface {
	NormalizeStream(r io.Reader) ([]NormalizedRecord, error)
}
