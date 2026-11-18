package app

import (
	"bytes"
	"database/sql"
	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeClaudeTranscript materializes a claude session jsonl at the path the
// claude adapter resolves for (workDir, sid), under the test's temp $HOME.
func writeClaudeJSONL(t *testing.T, workDir, sid, content string) {
	t.Helper()
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".claude", "projects", claude.EncodeCwd(workDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func removeClaudeTranscript(t *testing.T, workDir, sid string) {
	t.Helper()
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".claude", "projects", claude.EncodeCwd(workDir), sid+".jsonl")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
}

// enableTranscriptCopy opts the test into transcript copying (now OFF by
// default). Call it right after initTempFlowRoot in any test that exercises
// the capture path.
func enableTranscriptCopy(t *testing.T) {
	t.Helper()
	m := loadSettings()
	if err := settingSet(m, "transcripts.copy", true); err != nil {
		t.Fatal(err)
	}
	if err := saveSettings(m); err != nil {
		t.Fatal(err)
	}
}

func transcriptTask(slug, workDir, sid string) *flowdb.Task {
	return &flowdb.Task{
		Slug:      slug,
		WorkDir:   workDir,
		Harness:   sql.NullString{String: "claude", Valid: true},
		SessionID: sql.NullString{String: sid, Valid: true},
	}
}

func copyRecords(t *testing.T, slug, sid string) []harness.NormalizedRecord {
	t.Helper()
	copyPath, _, err := flowCopyPaths(slug, "claude", sid)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := readFlowCopyRecords(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func countRole(recs []harness.NormalizedRecord, role, text string) int {
	n := 0
	for _, r := range recs {
		if r.Role == role && (text == "" || r.Text == text) {
			n++
		}
	}
	return n
}

const line1 = `{"type":"user","timestamp":"2026-11-17T10:00:00Z","message":{"role":"user","content":"first prompt"}}` + "\n"
const line2 = `{"type":"assistant","timestamp":"2026-11-17T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"first answer"}]}}` + "\n"
const line3 = `{"type":"user","timestamp":"2026-11-17T10:00:02Z","message":{"role":"user","content":"second prompt"}}` + "\n"

func TestCaptureIncrementalOffsets(t *testing.T) {
	initTempFlowRoot(t)
	enableTranscriptCopy(t)
	wd := t.TempDir()
	task := transcriptTask("inc", wd, "sid-inc")

	writeClaudeJSONL(t, wd, "sid-inc", line1)
	n, _, err := captureTranscript(task)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("first capture: got %d records, want 1", n)
	}

	// A second capture with no change is a no-op.
	if n, _, _ := captureTranscript(task); n != 0 {
		t.Fatalf("no-change capture: got %d records, want 0", n)
	}

	// Append a turn — only the new lines are read.
	writeClaudeJSONL(t, wd, "sid-inc", line1+line2+line3)
	n, _, err = captureTranscript(task)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("incremental capture: got %d records, want 2", n)
	}

	recs := copyRecords(t, "inc", "sid-inc")
	if len(recs) != 3 {
		t.Fatalf("copy has %d records, want 3: %+v", len(recs), recs)
	}
	if countRole(recs, harness.RoleUser, "first prompt") != 1 || countRole(recs, harness.RoleUser, "second prompt") != 1 {
		t.Errorf("expected each prompt exactly once: %+v", recs)
	}
	// Turn numbering: first prompt turn 1, its answer turn 1, second prompt turn 2.
	if recs[0].Turn != 1 || recs[1].Turn != 1 || recs[2].Turn != 2 {
		t.Errorf("turns = %d,%d,%d, want 1,1,2", recs[0].Turn, recs[1].Turn, recs[2].Turn)
	}
}

func TestCapturePartialLineDeferred(t *testing.T) {
	initTempFlowRoot(t)
	enableTranscriptCopy(t)
	wd := t.TempDir()
	task := transcriptTask("partial", wd, "sid-p")

	// A trailing line with no newline (harness mid-write) must not be consumed.
	writeClaudeJSONL(t, wd, "sid-p", line1+`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"half`)
	n, _, _ := captureTranscript(task)
	if n != 1 {
		t.Fatalf("got %d records, want 1 (partial line deferred)", n)
	}
	// Complete the line — now it is captured.
	writeClaudeJSONL(t, wd, "sid-p", line1+line2)
	n, _, _ = captureTranscript(task)
	if n != 1 {
		t.Fatalf("got %d records after completion, want 1", n)
	}
}

// TestCaptureLargeSourceSeedsAtEOF verifies point 3: the first capture of a
// source above the hook threshold does NOT read it from byte 0 — it seeds the
// copy at EOF (empty copy, offset at the tail) and only later turns are
// captured incrementally. The pre-seed history is recoverable via backfill.
func TestCaptureLargeSourceSeedsAtEOF(t *testing.T) {
	initTempFlowRoot(t)
	t.Setenv("FLOW_TRANSCRIPT_HOOK_EOF_BYTES", "200") // tiny threshold for the test
	wd := t.TempDir()
	task := transcriptTask("big", wd, "sid-big")

	// A source already larger than the threshold at first sight.
	big := line1 + line2 + line3
	for len(big) < 400 {
		big += line2
	}
	writeClaudeJSONL(t, wd, "sid-big", big)

	n, _, err := captureTranscript(task)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("first capture of a large source: got %d records, want 0 (seeded at EOF)", n)
	}
	// The copy exists but is empty (no pre-seed history read in the hook).
	if recs := copyRecords(t, "big", "sid-big"); len(recs) != 0 {
		t.Fatalf("seeded copy should be empty, has %d records", len(recs))
	}

	// A NEW turn appended after the seed is captured incrementally.
	writeClaudeJSONL(t, wd, "sid-big", big+`{"type":"user","timestamp":"2026-11-17T11:00:00Z","message":{"role":"user","content":"after seed"}}`+"\n")
	n, _, err = captureTranscript(task)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("post-seed capture: got %d records, want 1", n)
	}
	recs := copyRecords(t, "big", "sid-big")
	if countRole(recs, harness.RoleUser, "after seed") != 1 {
		t.Fatalf("post-seed prompt not captured: %+v", recs)
	}
	if countRole(recs, harness.RoleUser, "first prompt") != 0 {
		t.Fatalf("pre-seed history must NOT be read in the hook: %+v", recs)
	}

	// `flow transcript backfill` re-reads the whole source and recovers the
	// pre-seed history.
	if _, _, _, err := backfillOne(task, false); err != nil {
		t.Fatal(err)
	}
	recs = copyRecords(t, "big", "sid-big")
	if countRole(recs, harness.RoleUser, "first prompt") != 1 {
		t.Fatalf("backfill should recover pre-seed history: %+v", recs)
	}
}

// TestCaptureSmallSourceReadsFromZero confirms the EOF fast path only triggers
// above the threshold: a small first source is still fully captured.
func TestCaptureSmallSourceReadsFromZero(t *testing.T) {
	initTempFlowRoot(t)
	t.Setenv("FLOW_TRANSCRIPT_HOOK_EOF_BYTES", "100000000") // 100 MB — never hit here
	wd := t.TempDir()
	task := transcriptTask("small", wd, "sid-small")
	writeClaudeJSONL(t, wd, "sid-small", line1+line2+line3)
	n, _, err := captureTranscript(task)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("small first capture: got %d records, want 3", n)
	}
}

func TestCaptureTruncationRebuilds(t *testing.T) {
	initTempFlowRoot(t)
	enableTranscriptCopy(t)
	wd := t.TempDir()
	task := transcriptTask("trunc", wd, "sid-t")

	writeClaudeJSONL(t, wd, "sid-t", line1+line2+line3)
	if _, _, err := captureTranscript(task); err != nil {
		t.Fatal(err)
	}
	// Truncate the source to a shorter transcript (offset now > size).
	writeClaudeJSONL(t, wd, "sid-t", line1)
	if _, _, err := captureTranscript(task); err != nil {
		t.Fatal(err)
	}
	recs := copyRecords(t, "trunc", "sid-t")
	if len(recs) != 1 || recs[0].Text != "first prompt" {
		t.Fatalf("after truncation copy should be rebuilt to 1 record: %+v", recs)
	}
}

func TestCaptureRotationRebuilds(t *testing.T) {
	initTempFlowRoot(t)
	enableTranscriptCopy(t)
	wd := t.TempDir()
	task := transcriptTask("rot", wd, "sid-r")

	writeClaudeJSONL(t, wd, "sid-r", line1+line2)
	if _, _, err := captureTranscript(task); err != nil {
		t.Fatal(err)
	}
	// Rotation: the harness replaces the file (new inode) with fresh content.
	removeClaudeTranscript(t, wd, "sid-r")
	writeClaudeJSONL(t, wd, "sid-r", line3)
	if _, _, err := captureTranscript(task); err != nil {
		t.Fatal(err)
	}
	recs := copyRecords(t, "rot", "sid-r")
	if len(recs) != 1 || recs[0].Text != "second prompt" {
		t.Fatalf("after rotation copy should be rebuilt: %+v", recs)
	}
}

func TestDedupePromptVsStopCapture(t *testing.T) {
	initTempFlowRoot(t)
	enableTranscriptCopy(t)
	wd := t.TempDir()
	task := transcriptTask("dedupe", wd, "sid-d")

	// UserPromptSubmit writes the prompt ahead of the turn.
	if err := captureUserPrompt(task, "first prompt"); err != nil {
		t.Fatal(err)
	}
	if got := copyRecords(t, "dedupe", "sid-d"); len(got) != 1 {
		t.Fatalf("after UPS append: %d records, want 1", len(got))
	}
	// The harness transcript now contains that same prompt plus the answer;
	// the Stop capture must NOT duplicate the prompt.
	writeClaudeJSONL(t, wd, "sid-d", line1+line2)
	if _, _, err := captureTranscript(task); err != nil {
		t.Fatal(err)
	}
	recs := copyRecords(t, "dedupe", "sid-d")
	if n := countRole(recs, harness.RoleUser, "first prompt"); n != 1 {
		t.Fatalf("prompt duplicated %d times, want 1: %+v", n, recs)
	}
	if countRole(recs, harness.RoleAssistant, "first answer") != 1 {
		t.Errorf("answer missing: %+v", recs)
	}
	// Turn numbering stays consistent across the UPS/Stop seam.
	for _, r := range recs {
		if r.Turn != 1 {
			t.Errorf("record out of turn 1: %+v", r)
		}
	}
}

func TestOptOutGlobalAndPerTask(t *testing.T) {
	initTempFlowRoot(t)
	wd := t.TempDir()

	// Global off.
	m := loadSettings()
	if err := settingSet(m, "transcripts.copy", false); err != nil {
		t.Fatal(err)
	}
	if err := saveSettings(m); err != nil {
		t.Fatal(err)
	}
	task := transcriptTask("optout", wd, "sid-o")
	writeClaudeJSONL(t, wd, "sid-o", line1)
	if n, _, _ := captureTranscript(task); n != 0 {
		t.Fatalf("global opt-out: captured %d, want 0", n)
	}
	if _, ok := flowCopyPathForTask(task); ok {
		t.Fatal("global opt-out: a copy was written")
	}

	// Per-task override back ON for one task.
	m = loadSettings()
	settingSet(m, "transcripts.tasks.optin.copy", true)
	saveSettings(m)
	task2 := transcriptTask("optin", wd, "sid-oi")
	writeClaudeJSONL(t, wd, "sid-oi", line1)
	if n, _, _ := captureTranscript(task2); n != 1 {
		t.Fatalf("per-task opt-in: captured %d, want 1", n)
	}
}

func TestBackfillIdempotent(t *testing.T) {
	initTempFlowRoot(t)
	wd := t.TempDir()
	task := transcriptTask("bf", wd, "sid-bf")
	writeClaudeJSONL(t, wd, "sid-bf", line1+line2+line3)

	n1, _, status, err := backfillOne(task, false)
	if err != nil || status != "" {
		t.Fatalf("backfill 1: n=%d status=%q err=%v", n1, status, err)
	}
	first := copyRecords(t, "bf", "sid-bf")

	n2, _, _, err := backfillOne(task, false)
	if err != nil {
		t.Fatal(err)
	}
	second := copyRecords(t, "bf", "sid-bf")
	if n1 != n2 || len(first) != len(second) {
		t.Fatalf("backfill not idempotent: n1=%d n2=%d len1=%d len2=%d", n1, n2, len(first), len(second))
	}
	if len(second) != 3 {
		t.Fatalf("expected 3 records, got %d", len(second))
	}
}

func TestBackfillDryRunWritesNothing(t *testing.T) {
	initTempFlowRoot(t)
	wd := t.TempDir()
	task := transcriptTask("dry", wd, "sid-dry")
	writeClaudeJSONL(t, wd, "sid-dry", line1+line2)

	n, _, _, err := backfillOne(task, true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("dry-run report: %d records, want 2", n)
	}
	if _, ok := flowCopyPathForTask(task); ok {
		t.Fatal("dry-run wrote a copy")
	}
}

func TestPurgeRemovesCopies(t *testing.T) {
	initTempFlowRoot(t)
	enableTranscriptCopy(t)
	wd := t.TempDir()
	task := transcriptTask("purge-me", wd, "sid-pm")
	writeClaudeJSONL(t, wd, "sid-pm", line1)
	if _, _, err := captureTranscript(task); err != nil {
		t.Fatal(err)
	}
	if _, ok := flowCopyPathForTask(task); !ok {
		t.Fatal("expected a copy before purge")
	}
	n, err := purgeTranscriptsFor("purge-me")
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("purge removed 0 files")
	}
	if _, ok := flowCopyPathForTask(task); ok {
		t.Fatal("copy still present after purge")
	}
}

func TestReaderFallbackToFlowCopy(t *testing.T) {
	initTempFlowRoot(t)
	enableTranscriptCopy(t)
	wd := t.TempDir()
	task := transcriptTask("fallback", wd, "sid-fb")
	writeClaudeJSONL(t, wd, "sid-fb", line1+line2)
	if _, _, err := captureTranscript(task); err != nil {
		t.Fatal(err)
	}
	// The harness deletes its transcript; the flow copy must still render.
	removeClaudeTranscript(t, wd, "sid-fb")

	copyPath, ok := flowCopyPathForTask(task)
	if !ok {
		t.Fatal("no flow copy to fall back to")
	}
	var buf bytes.Buffer
	if err := renderFlowCopy(copyPath, false, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"first prompt", "first answer", "─── User ───", "─── Assistant ───"} {
		if !strings.Contains(out, want) {
			t.Fatalf("flow-copy render missing %q:\n%s", want, out)
		}
	}
}

func TestCaptureSafeNeverErrorsOnMissingTranscript(t *testing.T) {
	initTempFlowRoot(t)
	wd := t.TempDir()
	task := transcriptTask("missing", wd, "sid-missing")
	// No source transcript on disk at all — must not panic or write.
	captureTranscriptSafe(task)
	captureUserPromptSafe(task, "") // empty prompt is a no-op too
	if _, ok := flowCopyPathForTask(task); ok {
		t.Fatal("capture wrote a copy with no source transcript")
	}
}

// TestInjectedUserTurnsFlaggedAndNotCounted pins #5 at the capture layer: a
// <task-notification> and a skill body that arrive as "user" records are
// flagged injected (via match's dropPrefixes, the shared source of truth) and
// therefore do NOT advance the turn counter — genuine prompts keep clean,
// monotonic turn numbers.
func TestInjectedUserTurnsFlaggedAndNotCounted(t *testing.T) {
	initTempFlowRoot(t)
	enableTranscriptCopy(t)
	wd := t.TempDir()
	task := transcriptTask("inj2", wd, "sid-inj2")

	real1 := `{"type":"user","timestamp":"2026-11-17T10:00:00Z","message":{"role":"user","content":"real one"}}` + "\n"
	ans := `{"type":"assistant","timestamp":"2026-11-17T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}` + "\n"
	taskNote := `{"type":"user","timestamp":"2026-11-17T10:00:02Z","message":{"role":"user","content":"<task-notification>mail landed</task-notification>"}}` + "\n"
	skillBody := `{"type":"user","timestamp":"2026-11-17T10:00:03Z","message":{"role":"user","content":"Base directory for this skill: /x\nstuff"}}` + "\n"
	real2 := `{"type":"user","timestamp":"2026-11-17T10:00:04Z","message":{"role":"user","content":"real two"}}` + "\n"

	writeClaudeJSONL(t, wd, "sid-inj2", real1+ans+taskNote+skillBody+real2)
	if _, _, err := captureTranscript(task); err != nil {
		t.Fatal(err)
	}
	recs := copyRecords(t, "inj2", "sid-inj2")

	turnOf := map[string]int{}
	for _, r := range recs {
		if r.Role != harness.RoleUser {
			continue
		}
		switch {
		case r.Text == "real one" || r.Text == "real two":
			if r.Injected {
				t.Errorf("genuine prompt wrongly flagged injected: %q", r.Text)
			}
			turnOf[r.Text] = r.Turn
		default:
			if !r.Injected {
				t.Errorf("injected user turn not flagged: %q", r.Text)
			}
		}
	}
	if turnOf["real one"] != 1 || turnOf["real two"] != 2 {
		t.Fatalf("turn numbering skewed by injected turns: real one=%d real two=%d (want 1,2)",
			turnOf["real one"], turnOf["real two"])
	}
}

func TestInjectedPromptNotWrittenByUPS(t *testing.T) {
	initTempFlowRoot(t)
	enableTranscriptCopy(t)
	wd := t.TempDir()
	task := transcriptTask("inj", wd, "sid-inj")
	// A wake push arriving as a prompt must not be recorded as a user turn.
	if err := captureUserPrompt(task, harness.WakeMarker+" mail landed"); err != nil {
		t.Fatal(err)
	}
	if _, ok := flowCopyPathForTask(task); ok {
		t.Fatal("UPS wrote an injected wake prompt")
	}
}
