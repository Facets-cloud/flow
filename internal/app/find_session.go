package app

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// claudeProjectsDir returns ~/.claude/projects. Overridden in tests.
var claudeProjectsDir = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// cmdFindSession implements `flow find-session <marker>`. It scans every
// session JSONL under ~/.claude/projects/*/*.jsonl for a line containing
// the marker and prints the basename (without `.jsonl`) of the first
// matching file — that basename is the Claude session ID.
//
// Intended use: a Claude session that wants to learn its own session_id
// (for binding an in-flight, ad-hoc session to a flow task) emits a
// unique marker in one Bash tool call, then invokes `flow find-session`
// in a SEPARATE Bash tool call (so the first call's stdout has been
// flushed to the JSONL transcript).
//
// Errors out with a clear message when zero or multiple files match.
func cmdFindSession(args []string) int {
	fs := flagSet("find-session")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "error: find-session requires a marker string")
		return 2
	}
	marker := fs.Arg(0)
	if len(marker) < 6 {
		fmt.Fprintln(os.Stderr, "error: marker is too short to be unique (use at least 6 chars)")
		return 2
	}

	root, err := claudeProjectsDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: locate claude projects dir: %v\n", err)
		return 1
	}

	matches, err := scanForMarker(root, marker)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(matches) == 0 {
		fmt.Fprintf(os.Stderr, "error: no session jsonl contains marker %q\n", marker)
		return 1
	}
	if len(matches) > 1 {
		fmt.Fprintf(os.Stderr, "error: marker %q matches %d sessions; use a more unique marker\n", marker, len(matches))
		for _, m := range matches {
			fmt.Fprintf(os.Stderr, "  %s\n", m)
		}
		return 1
	}
	// Strip .jsonl to get the session UUID.
	base := filepath.Base(matches[0])
	uuid := strings.TrimSuffix(base, ".jsonl")
	fmt.Println(uuid)
	return 0
}

// scanForMarker walks every *.jsonl file under root/<dir>/*.jsonl and
// returns paths to files that contain marker on any line. Uses a buffered
// line reader rather than loading whole files; jsonl files can be large.
func scanForMarker(root, marker string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	var hits []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		projDir := filepath.Join(root, e.Name())
		files, err := os.ReadDir(projDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".jsonl" {
				continue
			}
			full := filepath.Join(projDir, f.Name())
			if fileContains(full, marker) {
				hits = append(hits, full)
			}
		}
	}
	return hits, nil
}

// fileContains returns true if any line of path contains marker. Errors
// (open, scan) are silently treated as "no match" so a single unreadable
// file doesn't poison the whole scan.
func fileContains(path, marker string) bool {
	fh, err := os.Open(path)
	if err != nil {
		return false
	}
	defer fh.Close()
	scanner := bufio.NewScanner(fh)
	// JSONL lines can be long (multi-KB tool outputs). Bump the buffer.
	scanner.Buffer(make([]byte, 1<<16), 1<<24)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), marker) {
			return true
		}
	}
	return false
}
