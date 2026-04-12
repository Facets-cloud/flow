package app

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
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
// ~/.claude/projects/<dir> directory naming. Claude replaces / with -.
func EncodeCwdForClaude(cwd string) string {
	return strings.ReplaceAll(cwd, "/", "-")
}

// FindNewestSessionFile returns the stem of the most recently modified
// *.jsonl file in the given directory, or "" if none / directory missing.
// Kept for diagnostic / future use; not called by cmd_do in v2.
func FindNewestSessionFile(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var newest string
	var newestMtime int64
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		mtime := info.ModTime().UnixNano()
		if mtime > newestMtime {
			newestMtime = mtime
			newest = strings.TrimSuffix(e.Name(), ".jsonl")
		}
	}
	return newest
}
