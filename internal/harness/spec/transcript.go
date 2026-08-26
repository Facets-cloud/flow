package spec

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// blockTypeKey is the field both transcript formats flow has met use to
// discriminate content blocks. TranscriptMap.ToolBlock names the VALUE
// to look for here.
const blockTypeKey = "type"

// RenderTranscript decodes the harness's jsonl transcript and writes a
// normalized human-readable form to w.
//
// Output matches the shape flow already uses for claude — "─── User ───"
// / "─── Assistant ───" section headers with the body beneath — so a
// user reading `flow transcript` cannot tell which harness produced the
// session, which is the whole point.
//
// compact drops tool-call lines, leaving only the conversation.
func (a *Adapter) RenderTranscript(cwd, sessionID string, compact bool, w io.Writer) error {
	path, err := a.transcriptPath(cwd, sessionID)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("no %s transcript at %s: %w", a.spec.Name, path, err)
	}
	defer f.Close()

	m := a.spec.Transcript.Map
	sc := bufio.NewScanner(f)
	// Transcript lines carry whole turns and can be far larger than
	// bufio's 64KiB default; a truncated line would decode as invalid
	// JSON and silently drop real conversation.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	wrote := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			// A malformed line is skipped rather than fatal: a
			// transcript being appended to while we read it can end
			// in a partial record, and refusing to show the first
			// 200 good turns because of it would be worse.
			continue
		}

		if texts := lookup(rec, m.Text, nil); len(texts) > 0 {
			body := strings.TrimRight(strings.Join(texts, "\n"), "\n")
			if body != "" {
				fmt.Fprintf(w, "─── %s ───\n", roleLabel(lookup(rec, m.Role, nil)))
				fmt.Fprintln(w, body)
				wrote = true
			}
		}

		if compact || m.ToolBlock == "" || m.ToolName == "" {
			continue
		}
		isTool := func(el any) bool {
			obj, ok := el.(map[string]any)
			if !ok {
				return false
			}
			return obj[blockTypeKey] == m.ToolBlock
		}
		for _, name := range lookup(rec, m.ToolName, isTool) {
			if name == "" {
				continue
			}
			fmt.Fprintf(w, "─── tool: %s ───\n", name)
			wrote = true
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if !wrote {
		return fmt.Errorf("%s transcript at %s decoded to nothing — check [transcript.map] against the file's actual shape", a.spec.Name, path)
	}
	return nil
}

// roleLabel turns a raw role value into a section header.
func roleLabel(roles []string) string {
	if len(roles) == 0 {
		return "Message"
	}
	switch strings.ToLower(roles[0]) {
	case "user", "human":
		return "User"
	case "assistant", "model", "ai":
		return "Assistant"
	case "":
		return "Message"
	default:
		return strings.ToUpper(roles[0][:1]) + roles[0][1:]
	}
}

// lookup walks a dotted path into decoded JSON and returns every scalar
// it reaches.
//
// A segment ending in "[]" iterates an array, so one path can yield
// many values. filter, when non-nil, is applied to each array element
// and skips those that do not match — that is how a tool-name path
// selects only tool-call blocks out of a mixed content array.
//
// A path that does not resolve returns nothing. Records in a real
// transcript are heterogeneous, so "this record has no .text" is the
// normal case, not an error.
func lookup(v any, path string, filter func(any) bool) []string {
	if path == "" {
		return nil
	}
	return walk(v, strings.Split(path, "."), filter)
}

func walk(v any, segs []string, filter func(any) bool) []string {
	if len(segs) == 0 {
		if s, ok := scalar(v); ok {
			return []string{s}
		}
		return nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	seg, rest := segs[0], segs[1:]

	if key, isArray := strings.CutSuffix(seg, "[]"); isArray {
		arr, ok := obj[key].([]any)
		if !ok {
			return nil
		}
		var out []string
		for _, el := range arr {
			if filter != nil && !filter(el) {
				continue
			}
			out = append(out, walk(el, rest, filter)...)
		}
		return out
	}

	child, ok := obj[seg]
	if !ok {
		return nil
	}
	return walk(child, rest, filter)
}

// scalar renders a leaf value as text. Numbers come back from
// encoding/json as float64; formatting with %v would print 1e+06 for a
// large integer, so they are rendered without an exponent.
func scalar(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case bool:
		return fmt.Sprintf("%t", t), true
	case float64:
		return fmt.Sprintf("%g", t), true
	default:
		return "", false
	}
}
