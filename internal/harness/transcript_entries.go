package harness

// TranscriptEntry is one harness-neutral transcript event, the unit of
// `flow transcript --json`. Every adapter decodes its own on-disk format
// into this shape, so a consumer never reads harness-specific bytes.
//
// The JSON field names are a stable, documented contract (schema 1):
//
//	ts        RFC3339 timestamp from the harness record ("" when the
//	          harness records none)
//	role      "user" | "assistant" | "tool"
//	kind      "text" | "thinking" | "tool_call" | "tool_result"
//	text      the message text; for tool_call a one-line summary of the
//	          call's input; for tool_result the (truncated) output
//	tool      the tool name, on tool_call and (when known) tool_result
//	is_error  true on a failed tool_result
type TranscriptEntry struct {
	TS      string `json:"ts"`
	Role    string `json:"role"`
	Kind    string `json:"kind"`
	Text    string `json:"text"`
	Tool    string `json:"tool,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}

// Transcript entry roles and kinds.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"

	KindText       = "text"
	KindThinking   = "thinking"
	KindToolCall   = "tool_call"
	KindToolResult = "tool_result"
)

// TranscriptDecoder is an OPTIONAL harness capability: decode the
// session's on-disk transcript into normalised entries (the structured
// sibling of RenderTranscript). cwd and sessionID mean the same as for
// RenderTranscript; compact drops thinking and tool results.
type TranscriptDecoder interface {
	TranscriptEntries(cwd, sessionID string, compact bool) ([]TranscriptEntry, error)
}
