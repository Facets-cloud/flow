package app

import (
	"os"
	"path/filepath"
	"testing"
)

// stubClaudeProjects redirects claudeProjectsDir to a temp directory and
// pre-populates two project subdirs with jsonl files. Returns the root.
func stubClaudeProjects(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	old := claudeProjectsDir
	claudeProjectsDir = func() (string, error) { return root, nil }
	t.Cleanup(func() { claudeProjectsDir = old })
	return root
}

func writeJSONL(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCmdFindSessionHappyPath(t *testing.T) {
	root := stubClaudeProjects(t)
	const wantUUID = "11111111-2222-4333-8444-555555555555"
	writeJSONL(t,
		filepath.Join(root, "-Users-rohit-flow", wantUUID+".jsonl"),
		`{"sessionId":"11111111","content":"hello FLOW_TEST_MARKER_unique"}`+"\n",
	)
	writeJSONL(t,
		filepath.Join(root, "-Users-rohit-other", "ffffffff-eeee-4ddd-8ccc-bbbbbbbbbbbb.jsonl"),
		`{"content":"unrelated stuff"}`+"\n",
	)

	if rc := cmdFindSession([]string{"FLOW_TEST_MARKER_unique"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
}

func TestCmdFindSessionNoMatch(t *testing.T) {
	root := stubClaudeProjects(t)
	writeJSONL(t,
		filepath.Join(root, "-Users-rohit-flow", "11111111-2222-4333-8444-555555555555.jsonl"),
		`{"content":"only contains foo"}`+"\n",
	)
	if rc := cmdFindSession([]string{"NONEXISTENT_MARKER_zzz"}); rc != 1 {
		t.Errorf("rc=%d, want 1 for no match", rc)
	}
}

func TestCmdFindSessionMultipleMatchesError(t *testing.T) {
	root := stubClaudeProjects(t)
	writeJSONL(t,
		filepath.Join(root, "-Users-rohit-flow", "11111111-2222-4333-8444-555555555555.jsonl"),
		`{"content":"AMBIG_MARKER_xy"}`+"\n",
	)
	writeJSONL(t,
		filepath.Join(root, "-Users-rohit-other", "ffffffff-eeee-4ddd-8ccc-bbbbbbbbbbbb.jsonl"),
		`{"content":"AMBIG_MARKER_xy"}`+"\n",
	)
	if rc := cmdFindSession([]string{"AMBIG_MARKER_xy"}); rc != 1 {
		t.Errorf("rc=%d, want 1 when marker matches multiple sessions", rc)
	}
}

func TestCmdFindSessionRequiresMarker(t *testing.T) {
	stubClaudeProjects(t)
	if rc := cmdFindSession([]string{}); rc != 2 {
		t.Errorf("rc=%d, want 2 when no marker arg", rc)
	}
}

func TestCmdFindSessionRejectsTooShortMarker(t *testing.T) {
	stubClaudeProjects(t)
	if rc := cmdFindSession([]string{"abc"}); rc != 2 {
		t.Errorf("rc=%d, want 2 for too-short marker", rc)
	}
}

func TestCmdFindSessionMissingProjectsDir(t *testing.T) {
	old := claudeProjectsDir
	claudeProjectsDir = func() (string, error) {
		return filepath.Join(t.TempDir(), "definitely-not-here"), nil
	}
	t.Cleanup(func() { claudeProjectsDir = old })

	// Should report no match and return rc=1, not crash.
	if rc := cmdFindSession([]string{"any_marker_string"}); rc != 1 {
		t.Errorf("rc=%d, want 1 when projects dir is missing", rc)
	}
}
