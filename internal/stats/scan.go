// Package stats derives flow usage & ROI analytics from data already on
// disk: Claude session jsonl transcripts, flow.db, and the auto-runs /
// owner / kb directories. Nothing here writes to those sources.
package stats

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"time"
)

// LookupKind classifies a retrieval — a moment flow served stored context.
type LookupKind string

const (
	LookupResume    LookupKind = "resume"
	LookupReference LookupKind = "reference"
	LookupCrossTask LookupKind = "cross_task"
	LookupKB        LookupKind = "kb"
)

// Lookup is one retrieval event mined from a session jsonl.
type Lookup struct {
	Kind LookupKind `json:"kind"`
	TS   time.Time  `json:"ts"`
}

// Usage is a token tally summed across a session's assistant turns.
type Usage struct {
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	CacheCreation int64 `json:"cache_creation"`
	CacheRead     int64 `json:"cache_read"`
}

// Total is all tokens flow-managed sessions actually processed.
func (u Usage) Total() int64 { return u.Input + u.Output + u.CacheCreation + u.CacheRead }

// FileRollup is the per-jsonl scan result. It is JSON-serializable so the
// cache can persist it.
type FileRollup struct {
	Lookups []Lookup  `json:"lookups"`
	Usage   Usage     `json:"usage"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
}

// ---- raw jsonl shapes (only the fields we need) ----

type rawRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type rawMessage struct {
	Usage   *rawUsage       `json:"usage"`
	Content json.RawMessage `json:"content"`
}

type rawUsage struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
}

type rawBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type rawInput struct {
	Command  string `json:"command"`
	FilePath string `json:"file_path"`
}

// ScanJSONL reads a Claude session jsonl byte-stream and extracts a
// FileRollup. ownSlug is the slug of the task that owns this transcript,
// used to distinguish own-bootstrap reads (skipped) from sibling reads
// (cross_task). Malformed lines are skipped silently.
func ScanJSONL(r io.Reader, ownSlug string) (FileRollup, error) {
	var roll FileRollup
	ownPrefix := "/.flow/tasks/" + ownSlug + "/"

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec rawRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		ts := parseTS(rec.Timestamp)
		if !ts.IsZero() {
			if roll.First.IsZero() || ts.Before(roll.First) {
				roll.First = ts
			}
			if ts.After(roll.Last) {
				roll.Last = ts
			}
		}
		if len(rec.Message) == 0 {
			continue
		}
		var msg rawMessage
		if err := json.Unmarshal(rec.Message, &msg); err != nil {
			continue
		}
		if msg.Usage != nil {
			roll.Usage.Input += msg.Usage.Input
			roll.Usage.Output += msg.Usage.Output
			roll.Usage.CacheCreation += msg.Usage.CacheCreation
			roll.Usage.CacheRead += msg.Usage.CacheRead
		}
		if len(msg.Content) == 0 {
			continue
		}
		var blocks []rawBlock
		if err := json.Unmarshal(msg.Content, &blocks); err != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type != "tool_use" || len(b.Input) == 0 {
				continue
			}
			var in rawInput
			if err := json.Unmarshal(b.Input, &in); err != nil {
				continue
			}
			if kind, ok := classify(b.Name, in, ownPrefix); ok {
				roll.Lookups = append(roll.Lookups, Lookup{Kind: kind, TS: ts})
			}
		}
	}
	if err := sc.Err(); err != nil {
		return roll, err
	}
	return roll, nil
}

// classify maps a tool_use block to a lookup kind. The bool is false when
// the block is not a lookup (or is an own-bootstrap read we skip).
func classify(name string, in rawInput, ownPrefix string) (LookupKind, bool) {
	switch name {
	case "Bash":
		c := in.Command
		switch {
		case strings.Contains(c, "flow show task"):
			return LookupResume, true
		case strings.Contains(c, "flow show "):
			return LookupReference, true
		case strings.Contains(c, "flow transcript"):
			return LookupCrossTask, true
		}
	case "Read":
		p := in.FilePath
		switch {
		case strings.Contains(p, "/.flow/kb/"):
			return LookupKB, true
		case strings.Contains(p, ownPrefix):
			return "", false // own bootstrap read; already counted
		case strings.Contains(p, "/.flow/tasks/"), strings.Contains(p, "/.flow/projects/"):
			return LookupCrossTask, true
		}
	}
	return "", false
}

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Time{}
}
