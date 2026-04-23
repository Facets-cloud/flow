package app

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// newUUID returns a new UUID v4 in the standard 8-4-4-4-12 hex format.
// `flow do` pre-generates a UUID, claims it in the DB via an optimistic
// UPDATE, and then passes it to `claude --session-id` so the spawned
// session is created with exactly that UUID. No stream parsing, no
// filesystem polling, no post-hoc capture.
//
// Overridable in tests so concurrency tests can inject deterministic IDs.
var newUUID = func() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("rand read: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // v4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// EncodeCwdForClaude encodes an absolute cwd path for Claude Code's
// ~/.claude/projects/<dir> directory naming. Used by `flow transcript`
// to locate a known session_id's jsonl on disk.
//
// Rule (derived empirically by scanning ~/.claude/projects/* against the
// original cwd recorded inside each dir's *.jsonl files — CC's source is
// not public): the characters `/`, `.`, and `_` are each replaced by `-`.
// Other characters pass through unchanged. Samples:
//
//	/Users/alice/code/myapp                      → -Users-alice-code-myapp
//	/Users/alice/.flow/tasks/foo/workspace       → -Users-alice--flow-tasks-foo-workspace
//	/Users/alice/.cache/work/_default            → -Users-alice--cache-work--default
//	/Users/alice/monorepo/.../1_input_instance   → ...-1-input-instance
//
// If CC introduces a new substitution in a future version, add the char
// here and update TestEncodeCwdForClaude with a sample confirming it.
func EncodeCwdForClaude(cwd string) string {
	r := strings.NewReplacer("/", "-", ".", "-", "_", "-")
	return r.Replace(cwd)
}
