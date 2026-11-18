package app

import (
	"flow/internal/harness"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestConfigSetUnknownKeyRefusedWithHint pins that a typo'd key is refused and
// the error points at the intended key (the review's transcript.copy →
// transcripts.copy case).
func TestConfigSetUnknownKeyRefusedWithHint(t *testing.T) {
	initTempFlowRoot(t)
	get := captureStderr(t)
	rc := cmdConfig([]string{"set", "transcript.copy", "true"})
	errOut := get()
	if rc != 1 {
		t.Fatalf("set unknown key rc=%d, want 1", rc)
	}
	if !strings.Contains(errOut, "did you mean") || !strings.Contains(errOut, "transcripts.copy") {
		t.Fatalf("expected a did-you-mean hint for transcripts.copy; got:\n%s", errOut)
	}
	// The dead key must NOT have been written.
	if _, ok := settingGet(loadSettings(), "transcript.copy"); ok {
		t.Fatal("a refused key was stored anyway")
	}
}

// TestConfigSetWildKeyNoMisleadingHint pins that an unrelated key is refused
// without a bogus suggestion.
func TestConfigSetWildKeyNoMisleadingHint(t *testing.T) {
	initTempFlowRoot(t)
	get := captureStderr(t)
	rc := cmdConfig([]string{"set", "completely.unrelated.thing", "1"})
	errOut := get()
	if rc != 1 {
		t.Fatalf("rc=%d, want 1", rc)
	}
	if strings.Contains(errOut, "did you mean") {
		t.Fatalf("unrelated key should get no suggestion; got:\n%s", errOut)
	}
}

// TestConfigSetTypeChecked pins that a value of the wrong type is refused.
func TestConfigSetTypeChecked(t *testing.T) {
	initTempFlowRoot(t)
	// A bool knob rejects a number.
	if rc := cmdConfig([]string{"set", "transcripts.copy", "5"}); rc != 1 {
		t.Fatalf("set bool=5 rc=%d, want 1", rc)
	}
	// An int knob rejects a non-numeric string.
	if rc := cmdConfig([]string{"set", "transcripts.max_tool_result_bytes", "lots"}); rc != 1 {
		t.Fatalf("set int=lots rc=%d, want 1", rc)
	}
	// The valid forms still work.
	if rc := cmdConfig([]string{"set", "transcripts.max_tool_result_bytes", "2048"}); rc != 0 {
		t.Fatalf("set int=2048 rc=%d, want 0", rc)
	}
	if got := transcriptsMaxToolResultBytes(); got != 2048 {
		t.Fatalf("cap = %d, want 2048", got)
	}
}

// TestConfigSetRefusesMalformedFile pins #4: `config set` on an unparseable
// settings.json refuses and never overwrites it.
func TestConfigSetRefusesMalformedFile(t *testing.T) {
	root := initTempFlowRoot(t)
	p := filepath.Join(root, "settings.json")
	bad := []byte("{ this is : not json ")
	if err := os.WriteFile(p, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	get := captureStderr(t)
	rc := cmdConfig([]string{"set", "transcripts.copy", "true"})
	errOut := get()
	if rc != 1 {
		t.Fatalf("set on malformed file rc=%d, want 1", rc)
	}
	if !strings.Contains(errOut, "not valid JSON") || !strings.Contains(errOut, "refuses to overwrite") {
		t.Fatalf("expected a refuse-and-explain error; got:\n%s", errOut)
	}
	// The file is byte-for-byte unchanged.
	after, _ := os.ReadFile(p)
	if string(after) != string(bad) {
		t.Fatalf("malformed settings.json was overwritten:\n%s", after)
	}
}

// TestTranscriptCopyFailsClosed pins that a malformed/unreadable settings.json
// disables copying rather than falling back to a permissive default.
func TestTranscriptCopyFailsClosed(t *testing.T) {
	root := initTempFlowRoot(t)
	// Even with copy explicitly ON, a later corruption must fail closed.
	enableTranscriptCopy(t)
	if !transcriptsCopyEnabled("t") {
		t.Fatal("precondition: copy should be on")
	}
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if transcriptsCopyEnabled("t") {
		t.Fatal("malformed settings.json must disable copy (fail closed)")
	}
}

// TestFirstEnableNotice pins that turning copy on prints a one-time notice
// naming where copies live and how to purge, and does not repeat once on.
func TestFirstEnableNotice(t *testing.T) {
	initTempFlowRoot(t)
	out := captureStdout(t, func() {
		if rc := cmdConfig([]string{"set", "transcripts.copy", "true"}); rc != 0 {
			t.Fatalf("enable rc=%d", rc)
		}
	})
	if !strings.Contains(out, "notice:") || !strings.Contains(out, "purge") {
		t.Fatalf("expected a first-enable notice mentioning purge; got:\n%s", out)
	}
	// Already on: setting it on again does not re-notice.
	out = captureStdout(t, func() { cmdConfig([]string{"set", "transcripts.copy", "true"}) })
	if strings.Contains(out, "notice:") {
		t.Fatalf("notice repeated when already enabled; got:\n%s", out)
	}
}

// TestToolResultCapDefault pins that the default 16 KiB cap truncates an
// oversized tool_result in the flow copy.
func TestToolResultCapDefault(t *testing.T) {
	initTempFlowRoot(t)
	enableTranscriptCopy(t)
	wd := t.TempDir()
	task := transcriptTask("cap", wd, "sid-cap")

	big := strings.Repeat("x", 40<<10) // 40 KiB tool result
	line := `{"type":"user","timestamp":"2026-11-17T10:00:00Z","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"tu","content":"` + big + `"}]}}` + "\n"
	writeClaudeJSONL(t, wd, "sid-cap", line1+line)
	if _, _, err := captureTranscript(task); err != nil {
		t.Fatal(err)
	}
	recs := copyRecords(t, "cap", "sid-cap")
	found := false
	for _, r := range recs {
		if r.Role == harness.RoleToolResult {
			found = true
			if len(r.Text) > (16<<10)+32 {
				t.Fatalf("tool_result not capped: %d bytes", len(r.Text))
			}
			if !strings.HasSuffix(r.Text, "…[truncated]") {
				t.Fatalf("capped tool_result should be marked truncated; got tail %q", lastRunes(r.Text, 16))
			}
		}
	}
	if !found {
		t.Fatalf("no tool_result record in copy: %+v", recs)
	}
}

// TestRetentionPrunesOldCopies pins that transcripts.retention_days prunes
// copies older than the cutoff while keeping fresh ones.
func TestRetentionPrunesOldCopies(t *testing.T) {
	initTempFlowRoot(t)
	enableTranscriptCopy(t)
	wd := t.TempDir()

	// Two tasks with copies: one old, one fresh.
	for _, slug := range []string{"old", "fresh"} {
		task := transcriptTask(slug, wd, "sid-"+slug)
		writeClaudeJSONL(t, wd, "sid-"+slug, line1+line2)
		if _, _, err := captureTranscript(task); err != nil {
			t.Fatal(err)
		}
	}
	// Age the "old" copy 10 days back.
	oldCopy, _, _ := flowCopyPaths("old", "claude", "sid-old")
	old := time.Now().AddDate(0, 0, -10)
	if err := os.Chtimes(oldCopy, old, old); err != nil {
		t.Fatal(err)
	}
	// retention 0 (default): no-op.
	if n, err := enforceTranscriptRetention(); err != nil || n != 0 {
		t.Fatalf("retention=0 should be a no-op; n=%d err=%v", n, err)
	}
	// Set a 7-day retention and enforce.
	if rc := cmdConfig([]string{"set", "transcripts.retention_days", "7"}); rc != 0 {
		t.Fatalf("set retention rc=%d", rc)
	}
	if _, err := enforceTranscriptRetention(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldCopy); !os.IsNotExist(err) {
		t.Fatalf("old copy should have been pruned, stat err=%v", err)
	}
	freshCopy, _, _ := flowCopyPaths("fresh", "claude", "sid-fresh")
	if _, err := os.Stat(freshCopy); err != nil {
		t.Fatalf("fresh copy should survive retention: %v", err)
	}
}

func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// TestTranscriptCopyPermsPrivate pins that copy files are 0600 inside 0700
// dirs, and that a pre-existing world-readable copy is tightened on the next
// write (the "fix existing files' modes on next write" requirement).
func TestTranscriptCopyPermsPrivate(t *testing.T) {
	initTempFlowRoot(t)
	enableTranscriptCopy(t)
	wd := t.TempDir()
	task := transcriptTask("perm", wd, "sid-perm")
	writeClaudeJSONL(t, wd, "sid-perm", line1+line2)
	if _, _, err := captureTranscript(task); err != nil {
		t.Fatal(err)
	}
	copyPath, statePath, _ := flowCopyPaths("perm", "claude", "sid-perm")
	if fi, err := os.Stat(copyPath); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != transcriptFileMode {
		t.Errorf("copy mode = %o, want %o", fi.Mode().Perm(), transcriptFileMode)
	}
	if fi, err := os.Stat(statePath); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != transcriptFileMode {
		t.Errorf("sidecar mode = %o, want %o", fi.Mode().Perm(), transcriptFileMode)
	}
	if fi, err := os.Stat(filepath.Dir(copyPath)); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != transcriptDirMode {
		t.Errorf("transcripts dir mode = %o, want %o", fi.Mode().Perm(), transcriptDirMode)
	}

	// Loosen the copy as an older flow would have (0644), append a turn, and
	// confirm the next write tightens it back to 0600.
	if err := os.Chmod(copyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	writeClaudeJSONL(t, wd, "sid-perm", line1+line2+line3)
	if _, _, err := captureTranscript(task); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(copyPath); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != transcriptFileMode {
		t.Errorf("copy mode after rewrite = %o, want %o (modes not tightened)", fi.Mode().Perm(), transcriptFileMode)
	}
}
