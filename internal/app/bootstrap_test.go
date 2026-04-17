package app

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewUUIDFormat(t *testing.T) {
	for i := 0; i < 50; i++ {
		id, err := newUUID()
		if err != nil {
			t.Fatalf("newUUID: %v", err)
		}
		if !uuidRe.MatchString(id) {
			t.Errorf("newUUID returned %q, does not match UUID v4 format", id)
		}
	}
}

func TestNewUUIDUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id, err := newUUID()
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("duplicate UUID after %d: %s", i, id)
		}
		seen[id] = true
	}
}

// TestEncodeCwdForClaude pins the empirical rule derived from
// ~/.claude/projects/* vs. the original cwd recorded in each dir's
// *.jsonl. `/`, `.`, and `_` each map to `-`; everything else is
// unchanged. If a new sample surfaces that needs a different rule, add
// the observed pair here before touching EncodeCwdForClaude.
func TestEncodeCwdForClaude(t *testing.T) {
	cases := []struct {
		cwd, want string
	}{
		// Plain path — only slashes transform.
		{"/Users/rohit/code/flow", "-Users-rohit-code-flow"},
		// Dotfile segment — the register-session regression from 2026-04-15.
		// `.flow` becomes `-flow`, producing a double dash after `rohit`.
		{"/Users/rohit/.flow/tasks/review-unni-prs/workspace",
			"-Users-rohit--flow-tasks-review-unni-prs-workspace"},
		// Underscores in a path segment also transform — observed on
		// facets-iac module paths.
		{"/Users/rohit/facets-iac/capillary-cloud-tf/modules/1_input_instance/application_gcp",
			"-Users-rohit-facets-iac-capillary-cloud-tf-modules-1-input-instance-application-gcp"},
		// Underscore-prefix dir — seen in paperclip workspace trees;
		// `/_default` becomes `--default`.
		{"/Users/rohit/.paperclip/instances/default/projects/abc/def/_default",
			"-Users-rohit--paperclip-instances-default-projects-abc-def--default"},
		// Hyphens, digits, and mixed case pass through unchanged.
		{"/Users/rohit/Downloads/coinswitch-charts-45dae5e1171f",
			"-Users-rohit-Downloads-coinswitch-charts-45dae5e1171f"},
	}
	for _, tc := range cases {
		if got := EncodeCwdForClaude(tc.cwd); got != tc.want {
			t.Errorf("EncodeCwdForClaude(%q) = %q, want %q", tc.cwd, got, tc.want)
		}
	}
}

// TestFindSessionByWorkDir exercises the fallback lookup used when
// EncodeCwdForClaude drifts from Claude Code's current rule. A jsonl
// whose first `cwd` record matches the requested work_dir is returned
// even if its containing dir is named arbitrarily.
func TestFindSessionByWorkDir(t *testing.T) {
	projects := t.TempDir()
	workDir := "/Users/rohit/.flow/tasks/foo/workspace"

	// Dir A: wrong cwd, newer mtime — must be ignored.
	dirA := filepath.Join(projects, "some-other-encoding")
	if err := os.MkdirAll(dirA, 0o755); err != nil {
		t.Fatal(err)
	}
	otherSid := "sid-other"
	if err := os.WriteFile(filepath.Join(dirA, otherSid+".jsonl"),
		[]byte(`{"type":"permission-mode"}`+"\n"+
			`{"cwd":"/some/other/path","type":"user"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Dir B: correct cwd, older mtime — must win over A.
	dirB := filepath.Join(projects, "arbitrary-name-ignored")
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatal(err)
	}
	wantSid := "sid-match"
	if err := os.WriteFile(filepath.Join(dirB, wantSid+".jsonl"),
		[]byte(`{"type":"permission-mode"}`+"\n"+
			`{"cwd":"`+workDir+`","type":"user"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	older := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dirB, wantSid+".jsonl"), older, older); err != nil {
		t.Fatal(err)
	}

	got := FindSessionByWorkDir(projects, workDir)
	if got != wantSid {
		t.Errorf("FindSessionByWorkDir = %q, want %q", got, wantSid)
	}

	// Negative: no match → empty.
	if got := FindSessionByWorkDir(projects, "/never/seen"); got != "" {
		t.Errorf("FindSessionByWorkDir(no match) = %q, want empty", got)
	}

	// Negative: missing projects root → empty.
	if got := FindSessionByWorkDir(filepath.Join(projects, "nope"), workDir); got != "" {
		t.Errorf("FindSessionByWorkDir(missing root) = %q, want empty", got)
	}
}

func TestFindNewestSessionFile(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "old-uuid.jsonl")
	b := filepath.Join(dir, "new-uuid.jsonl")
	if err := os.WriteFile(a, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(a, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := FindNewestSessionFile(dir); got != "new-uuid" {
		t.Errorf("got %q, want new-uuid", got)
	}
	if got := FindNewestSessionFile(filepath.Join(dir, "nope")); got != "" {
		t.Errorf("missing dir: got %q, want empty", got)
	}
}
