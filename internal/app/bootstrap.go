package app

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
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
// ~/.claude/projects/<dir> directory naming.
//
// Rule (derived empirically by scanning ~/.claude/projects/* against the
// original cwd recorded inside each dir's *.jsonl files — CC's source is
// not public): the characters `/`, `.`, and `_` are each replaced by `-`.
// Other characters pass through unchanged. Samples:
//
//	/Users/rohit/control-plane               → -Users-rohit-control-plane
//	/Users/rohit/.flow/tasks/foo/workspace   → -Users-rohit--flow-tasks-foo-workspace
//	/Users/rohit/.paperclip/.../_default     → -Users-rohit--paperclip-...--default
//	/Users/rohit/facets-iac/.../1_input_instance → ...-1-input-instance
//
// If CC introduces a new substitution in a future version, add the char
// here and update TestEncodeCwdForClaude with a sample confirming it.
func EncodeCwdForClaude(cwd string) string {
	r := strings.NewReplacer("/", "-", ".", "-", "_", "-")
	return r.Replace(cwd)
}

// FindNewestSessionFile returns the stem of the most recently modified
// *.jsonl file in the given directory, or "" if none / directory missing.
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

// FindSessionByWorkDir is a fallback lookup that doesn't rely on
// EncodeCwdForClaude matching CC's current encoding rule. It scans every
// subdir of ~/.claude/projects/, inspects the newest *.jsonl in each, and
// returns the stem of the newest jsonl whose first "cwd" field equals
// wantCwd. Returns "" if no match.
//
// Used by register-session as a safety net: if CC changes its path
// encoding in a future version, the primary encoded-dir lookup will
// miss, but this scan still finds the session by its recorded cwd.
func FindSessionByWorkDir(projectsRoot, wantCwd string) string {
	entries, err := os.ReadDir(projectsRoot)
	if err != nil {
		return ""
	}
	var winner string
	var winnerMtime int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subdir := filepath.Join(projectsRoot, e.Name())
		stem := FindNewestSessionFile(subdir)
		if stem == "" {
			continue
		}
		jsonlPath := filepath.Join(subdir, stem+".jsonl")
		cwd := readCwdFromJSONL(jsonlPath)
		if cwd != wantCwd {
			continue
		}
		info, err := os.Stat(jsonlPath)
		if err != nil {
			continue
		}
		mtime := info.ModTime().UnixNano()
		if mtime > winnerMtime {
			winnerMtime = mtime
			winner = stem
		}
	}
	return winner
}

// readCwdFromJSONL returns the first "cwd" string value found in a
// Claude Code session jsonl, or "" if none / unreadable. Scans a bounded
// prefix of the file (first 256 lines) so we never read a multi-MB
// transcript just to learn its working directory.
func readCwdFromJSONL(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for i := 0; i < 256 && sc.Scan(); i++ {
		var rec struct {
			Cwd string `json:"cwd"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			continue
		}
		if rec.Cwd != "" {
			return rec.Cwd
		}
	}
	return ""
}
