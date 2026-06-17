package stats

import (
	"strings"
	"testing"
)

func TestScanJSONL(t *testing.T) {
	// One assistant turn with usage, a flow-show-task bootstrap (resume),
	// a flow show reference, a flow transcript cross-task, a kb read,
	// an own-task brief read (skipped), and a sibling read (cross-task).
	lines := []string{
		`{"type":"assistant","timestamp":"2026-06-01T10:00:00.000Z","message":{"role":"assistant","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":2,"cache_read_input_tokens":100},"content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"assistant","timestamp":"2026-06-01T10:00:01.000Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"flow show task"}}]}}`,
		`{"type":"assistant","timestamp":"2026-06-01T10:00:02.000Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"flow show project flow"}}]}}`,
		`{"type":"assistant","timestamp":"2026-06-01T10:00:03.000Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"flow transcript sibling"}}]}}`,
		`{"type":"assistant","timestamp":"2026-06-01T10:00:04.000Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/Users/x/.flow/kb/org.md"}}]}}`,
		`{"type":"assistant","timestamp":"2026-06-01T10:00:05.000Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/Users/x/.flow/tasks/mine/brief.md"}}]}}`,
		`{"type":"assistant","timestamp":"2026-06-01T10:00:06.000Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/Users/x/.flow/tasks/other/brief.md"}}]}}`,
		`garbage-line-should-skip`,
	}
	roll, err := ScanJSONL(strings.NewReader(strings.Join(lines, "\n")), "mine")
	if err != nil {
		t.Fatalf("ScanJSONL: %v", err)
	}
	if roll.Usage.Total() != 117 {
		t.Errorf("Usage.Total = %d, want 117", roll.Usage.Total())
	}
	counts := map[LookupKind]int{}
	for _, l := range roll.Lookups {
		counts[l.Kind]++
	}
	if counts[LookupResume] != 1 || counts[LookupReference] != 1 ||
		counts[LookupCrossTask] != 2 || counts[LookupKB] != 1 {
		t.Errorf("lookup counts = %v, want resume1 reference1 cross_task2 kb1", counts)
	}
	if roll.First.IsZero() || roll.Last.Before(roll.First) {
		t.Errorf("First/Last not set correctly: %v..%v", roll.First, roll.Last)
	}
}
