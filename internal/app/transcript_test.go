package app

import (
	"flow/internal/flowdb"
	"flow/internal/harness/claude"
	"flow/internal/iterm"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Parser-level tests live in internal/harness/claude/transcript_test.go
// (the claude jsonl format is owned by the harness impl). The tests
// here exercise the cmdTranscript wiring: ref resolution, the "no
// session" gate, the happy-path dispatch into the harness, and the
// retrospective-bind full-render guarantee.

func TestTranscriptCmdNoSession(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("FLOW_ROOT", filepath.Join(tmp, "flow"))
	t.Setenv("HOME", tmp)

	oldOsa := iterm.Runner
	iterm.Runner = func(args []string) error { return nil }
	t.Cleanup(func() { iterm.Runner = oldOsa })

	cmdInit(nil)
	cmdAdd([]string{"task", "No Session Task", "--slug", "no-session"})

	rc := cmdTranscript([]string{"no-session"})
	if rc != 1 {
		t.Errorf("transcript with no session: rc=%d, want 1", rc)
	}
}

func TestTranscriptCmdWithSession(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("FLOW_ROOT", filepath.Join(tmp, "flow"))
	t.Setenv("HOME", tmp)

	oldOsa := iterm.Runner
	iterm.Runner = func(args []string) error { return nil }
	t.Cleanup(func() { iterm.Runner = oldOsa })

	cmdInit(nil)

	repo := filepath.Join(tmp, "code", "myrepo")
	os.MkdirAll(repo, 0o755)
	cmdAdd([]string{"task", "Transcript Test", "--slug", "tx-test", "--work-dir", repo})

	// Simulate session bootstrap.
	dbPath, _ := flowDBPath()
	db, _ := flowdb.OpenDB(dbPath)
	defer db.Close()

	sid := "deadbeef-1234-5678-9abc-def012345678"
	now := flowdb.NowISO()
	db.Exec(`UPDATE tasks SET session_id=?, session_started=?, updated_at=? WHERE slug=?`,
		sid, now, now, "tx-test")

	// Write a minimal jsonl at the claude convention path.
	encoded := claude.EncodeCwd(repo)
	sessionDir := filepath.Join(tmp, ".claude", "projects", encoded)
	os.MkdirAll(sessionDir, 0o755)
	os.WriteFile(
		filepath.Join(sessionDir, sid+".jsonl"),
		[]byte(`{"type":"user","message":{"role":"user","content":"test message"},"uuid":"u1","timestamp":"2026-04-12T10:00:00Z","sessionId":"`+sid+`"}`+"\n"),
		0o644,
	)

	rc := cmdTranscript([]string{"tx-test"})
	if rc != 0 {
		t.Errorf("transcript with session: rc=%d, want 0", rc)
	}
}

// TestTranscriptRetrospectiveBindFullOutput pins the bug this task
// fixes: a retrospective `flow do --here` bind stamps session_started
// at BIND time, which lands after all the real work in the jsonl. The
// transcript (and therefore the `flow done` close-out sweep) must still
// show the whole conversation — not just entries after the bind.
func TestTranscriptRetrospectiveBindFullOutput(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("FLOW_ROOT", filepath.Join(tmp, "flow"))
	t.Setenv("HOME", tmp)

	oldOsa := iterm.Runner
	iterm.Runner = func(args []string) error { return nil }
	t.Cleanup(func() { iterm.Runner = oldOsa })

	cmdInit(nil)

	repo := filepath.Join(tmp, "code", "myrepo")
	os.MkdirAll(repo, 0o755)
	cmdAdd([]string{"task", "Retro Bind", "--slug", "retro", "--work-dir", repo})

	dbPath, _ := flowDBPath()
	db, _ := flowdb.OpenDB(dbPath)
	defer db.Close()

	sid := "deadbeef-1234-5678-9abc-def012345678"
	// session_started is stamped LATE (bind time) — after all the work
	// below. This is exactly the retrospective `--here` scenario.
	bindTime := "2026-05-29T18:00:00+05:30"
	db.Exec(`UPDATE tasks SET session_id=?, session_started=?, updated_at=? WHERE slug=?`,
		sid, bindTime, bindTime, "retro")

	encoded := claude.EncodeCwd(repo)
	sessionDir := filepath.Join(tmp, ".claude", "projects", encoded)
	os.MkdirAll(sessionDir, 0o755)
	// All entries predate the bind time by hours — the real work.
	jsonl := `{"type":"user","message":{"role":"user","content":"EARLY WORK before the bind"},"uuid":"u1","timestamp":"2026-05-29T09:00:00.000Z","sessionId":"` + sid + `"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"EARLY REPLY before the bind"}]},"uuid":"a1","timestamp":"2026-05-29T09:01:00.000Z","sessionId":"` + sid + `"}
`
	os.WriteFile(filepath.Join(sessionDir, sid+".jsonl"), []byte(jsonl), 0o644)

	out := captureStdout(t, func() {
		if rc := cmdTranscript([]string{"retro"}); rc != 0 {
			t.Errorf("transcript rc=%d, want 0", rc)
		}
	})

	if !strings.Contains(out, "EARLY WORK before the bind") {
		t.Errorf("pre-bind work elided from transcript on retrospective bind:\n%s", out)
	}
	if !strings.Contains(out, "EARLY REPLY before the bind") {
		t.Errorf("pre-bind assistant reply elided from transcript:\n%s", out)
	}
}
