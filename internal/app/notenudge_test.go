package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness"
)

// ---------- helpers ----------

// bindNudgeTask adds an in-progress task bound to sid, with session_started
// set to startedISO (the no-notes reference instant).
func bindNudgeTask(t *testing.T, slug, sid, startedISO string) {
	t.Helper()
	if rc := cmdAdd([]string{"task", slug, "--slug", slug}); rc != 0 {
		t.Fatalf("add %s rc=%d", slug, rc)
	}
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, status='in-progress', session_started=? WHERE slug=?`,
		sid, startedISO, slug,
	); err != nil {
		t.Fatalf("bind %s: %v", slug, err)
	}
}

// upsNudge fires the UserPromptSubmit hook once for session sid with the
// given human prompt and returns the injected additionalContext ("" if the
// hook emitted nothing).
func upsNudge(t *testing.T, sid, prompt string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"session_id": sid, "prompt": prompt})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	withHookStdin(t, string(payload), func() {
		raw = captureStdout(t, func() {
			if rc := cmdHookUserPromptSubmit(nil); rc != 0 {
				t.Fatalf("ups rc=%d", rc)
			}
		})
	})
	// readHookPayload leaves the decoded session id / prompt in package
	// globals; clear them so a later test that reads currentSessionID()
	// without going through a hook payload doesn't see this session.
	hookSessionID, hookPrompt, hookPromptKnown = "", "", false
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var parsed struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("parse ups output: %v\nraw: %s", err, raw)
	}
	return parsed.HookSpecificOutput.AdditionalContext
}

// nudged reports whether ctx carries the progress-note nudge for slug.
func nudged(ctx, slug string) bool {
	return strings.Contains(ctx, "turns since the last progress note on "+slug)
}

// disableCopy turns the transcript copy off (exercise the fallback counter).
func disableCopy(t *testing.T) {
	t.Helper()
	if rc := cmdConfig([]string{"set", "transcripts.copy", "off"}); rc != 0 {
		t.Fatalf("disable copy rc=%d", rc)
	}
}

// writeCopyRecords writes recs as the flow transcript copy for (slug, sid).
func writeCopyRecords(t *testing.T, slug, sid string, recs []harness.NormalizedRecord) {
	t.Helper()
	copyPath, _, err := flowCopyPaths(slug, "claude", sid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for i := range recs {
		if err := enc.Encode(&recs[i]); err != nil {
			t.Fatal(err)
		}
	}
}

func past(d time.Duration) string {
	return time.Now().Add(-d).Format(time.RFC3339)
}

// ---------- the compounding threshold math ----------

func TestNoteNudgeThresholdSequence(t *testing.T) {
	// base 15, step 0.2 → ceil(15·1.2^k): 15, 18, 22, 26, 32, 38, …
	var fires []int
	last := 0
	for count := 1; count <= 40; count++ {
		th, ok := noteNudgeThreshold(count, 15, 0.2)
		if ok && th > last {
			last = th
			fires = append(fires, count) // +1 stepping ⇒ count == threshold
		}
	}
	want := []int{15, 18, 22, 26, 32, 38}
	if fmt.Sprint(fires) != fmt.Sprint(want) {
		t.Errorf("threshold crossings = %v, want %v", fires, want)
	}
}

func TestNoteNudgeThresholdBelowBase(t *testing.T) {
	for count := 0; count < 15; count++ {
		if _, ok := noteNudgeThreshold(count, 15, 0.2); ok {
			t.Fatalf("count %d: should not reach base", count)
		}
	}
}

func TestNoteNudgeThresholdNonPositiveStep(t *testing.T) {
	// step 0 ⇒ a single threshold at base; never advances.
	for _, count := range []int{15, 16, 50, 1000} {
		th, ok := noteNudgeThreshold(count, 15, 0)
		if !ok || th != 15 {
			t.Errorf("count %d: got (%d,%v), want (15,true)", count, th, ok)
		}
	}
}

func TestNoteNudgeThresholdJumpFiresHighestOnce(t *testing.T) {
	// A multi-turn jump past two thresholds reports the highest (once).
	th, ok := noteNudgeThreshold(20, 15, 0.2)
	if !ok || th != 18 {
		t.Errorf("jump to 20: got (%d,%v), want (18,true)", th, ok)
	}
}

// ---------- fallback counter: fires once per crossing ----------

func TestNoteNudgeFiresOncePerCrossing(t *testing.T) {
	setupFlowRoot(t)
	disableCopy(t)
	const sid = "11111111-1111-4111-8111-111111111111"
	bindNudgeTask(t, "nudge-seq", sid, past(time.Hour))

	var fires []int
	for turn := 1; turn <= 40; turn++ {
		ctx := upsNudge(t, sid, fmt.Sprintf("human prompt %d", turn))
		if nudged(ctx, "nudge-seq") {
			fires = append(fires, turn)
			// The line reports the live turn count.
			if want := fmt.Sprintf("flow: %d turns since the last progress note on nudge-seq", turn); !strings.Contains(ctx, want) {
				t.Errorf("turn %d: nudge text missing %q; got:\n%s", turn, want, ctx)
			}
		}
	}
	want := []int{15, 18, 22, 26, 32, 38}
	if fmt.Sprint(fires) != fmt.Sprint(want) {
		t.Errorf("fired at turns %v, want %v", fires, want)
	}
}

// ---------- reset on a new note ----------

func TestNoteNudgeResetOnNewNote(t *testing.T) {
	setupFlowRoot(t)
	disableCopy(t)
	const sid = "22222222-2222-4222-8222-222222222222"
	bindNudgeTask(t, "nudge-reset", sid, past(time.Hour))

	// Drive to the first nudge.
	for turn := 1; turn <= 15; turn++ {
		if ctx := upsNudge(t, sid, "p"); nudged(ctx, "nudge-reset") && turn != 15 {
			t.Fatalf("unexpected early nudge at turn %d", turn)
		}
	}

	// Write a progress note: its mtime becomes the new reference and resets
	// the counter.
	root, _ := flowRoot()
	updDir := filepath.Join(root, "tasks", "nudge-reset", "updates")
	if err := os.MkdirAll(updDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(updDir, "2026-11-17-note.md"), []byte("done stuff\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The next 14 turns are below base again (no nudge); the 15th fires.
	for turn := 1; turn <= 14; turn++ {
		if ctx := upsNudge(t, sid, "p"); nudged(ctx, "nudge-reset") {
			t.Fatalf("nudge fired %d turns after the note (should reset)", turn)
		}
	}
	ctx := upsNudge(t, sid, "p")
	if !nudged(ctx, "nudge-reset") {
		t.Fatalf("expected a fresh nudge 15 turns after the note; got:\n%s", ctx)
	}
}

// ---------- disabled globally / per task ----------

func TestNoteNudgeDisabledGlobally(t *testing.T) {
	setupFlowRoot(t)
	disableCopy(t)
	if rc := cmdConfig([]string{"set", "nudge.notes.enabled", "off"}); rc != 0 {
		t.Fatalf("disable rc=%d", rc)
	}
	const sid = "33333333-3333-4333-8333-333333333333"
	bindNudgeTask(t, "nudge-off", sid, past(time.Hour))
	for turn := 1; turn <= 30; turn++ {
		if ctx := upsNudge(t, sid, "p"); nudged(ctx, "nudge-off") {
			t.Fatalf("nudge fired at turn %d while globally disabled", turn)
		}
	}
}

func TestNoteNudgeDisabledPerTask(t *testing.T) {
	setupFlowRoot(t)
	disableCopy(t)
	// Global on (default); this one task off.
	if rc := cmdConfig([]string{"set", "nudge.notes.tasks.nudge-pt.enabled", "off"}); rc != 0 {
		t.Fatalf("per-task disable rc=%d", rc)
	}
	const sid = "44444444-4444-4444-8444-444444444444"
	bindNudgeTask(t, "nudge-pt", sid, past(time.Hour))
	for turn := 1; turn <= 30; turn++ {
		if ctx := upsNudge(t, sid, "p"); nudged(ctx, "nudge-pt") {
			t.Fatalf("nudge fired at turn %d while per-task disabled", turn)
		}
	}
}

// ---------- auto runs and unbound sessions are skipped ----------

func TestNoteNudgeAutoRunSkipped(t *testing.T) {
	setupFlowRoot(t)
	disableCopy(t)
	const sid = "55555555-5555-4555-8555-555555555555"
	bindNudgeTask(t, "nudge-auto", sid, past(time.Hour))
	t.Setenv(autoRunEnvVar, "nudge-auto") // headless auto run
	for turn := 1; turn <= 30; turn++ {
		if ctx := upsNudge(t, sid, "p"); nudged(ctx, "nudge-auto") {
			t.Fatalf("nudge fired at turn %d inside an auto run", turn)
		}
	}
}

func TestNoteNudgeUnboundSkipped(t *testing.T) {
	setupFlowRoot(t)
	disableCopy(t)
	// No task carries this session id.
	const sid = "66666666-6666-4666-8666-666666666666"
	for turn := 1; turn <= 30; turn++ {
		if ctx := upsNudge(t, sid, "p"); nudged(ctx, "") || strings.Contains(ctx, "progress note") {
			t.Fatalf("nudge fired at turn %d on an unbound session", turn)
		}
	}
}

// ---------- injected prompts are never counted ----------

func TestNoteNudgeInjectedNotCounted(t *testing.T) {
	setupFlowRoot(t)
	disableCopy(t)
	const sid = "77777777-7777-4777-8777-777777777777"
	bindNudgeTask(t, "nudge-inj", sid, past(time.Hour))
	// 14 human turns, then a flood of injected (wake) prompts: none advance
	// the counter, so no nudge yet.
	for turn := 1; turn <= 14; turn++ {
		if ctx := upsNudge(t, sid, "p"); nudged(ctx, "nudge-inj") {
			t.Fatalf("early nudge at human turn %d", turn)
		}
	}
	wake := harness.WakeMarker + " mail arrived"
	for i := 0; i < 10; i++ {
		if ctx := upsNudge(t, sid, wake); nudged(ctx, "nudge-inj") {
			t.Fatalf("an injected prompt advanced the counter / fired a nudge")
		}
	}
	// The 15th human turn crosses base.
	if ctx := upsNudge(t, sid, "p"); !nudged(ctx, "nudge-inj") {
		t.Fatalf("expected a nudge on the 15th human turn; got:\n%s", ctx)
	}
}

func TestCountUserTurnsSinceFiltersAndRespectsBoundary(t *testing.T) {
	setupFlowRoot(t)
	const sid = "88888888-8888-4888-8888-888888888888"
	ref := time.Now().Add(-30 * time.Minute)
	before := ref.Add(-5 * time.Minute).Format(time.RFC3339)
	after := ref.Add(5 * time.Minute).Format(time.RFC3339)
	recs := []harness.NormalizedRecord{
		{Role: harness.RoleUser, TS: before, Text: "old"},                 // before ref — skip
		{Role: harness.RoleUser, TS: after, Text: "u1"},                   // count
		{Role: harness.RoleUser, TS: after, Text: "u2"},                   // count
		{Role: harness.RoleUser, TS: after, Injected: true, Text: "wake"}, // injected — skip
		{Role: harness.RoleAssistant, TS: after, Text: "a"},               // not user — skip
		{Role: harness.RoleToolCall, TS: after, Text: "t"},                // not user — skip
		{Role: harness.RoleUser, TS: after, Text: "u3"},                   // count
	}
	writeCopyRecords(t, "count-task", sid, recs)
	copyPath, _, _ := flowCopyPaths("count-task", "claude", sid)
	if got := countUserTurnsSince(copyPath, ref); got != 3 {
		t.Errorf("countUserTurnsSince = %d, want 3", got)
	}
}

// TestCountUserTurnsFromIncremental checks the incremental counter reads only
// the appended bytes on the second call and advances its offset (point 6).
func TestCountUserTurnsFromIncremental(t *testing.T) {
	setupFlowRoot(t)
	const sid = "77777777-7777-4777-8777-777777777777"
	ref := time.Now().Add(-30 * time.Minute)
	after := ref.Add(5 * time.Minute).Format(time.RFC3339)
	recs := []harness.NormalizedRecord{
		{Role: harness.RoleUser, TS: after, Text: "u1"},
		{Role: harness.RoleAssistant, TS: after, Text: "a"},
		{Role: harness.RoleUser, TS: after, Text: "u2"},
	}
	writeCopyRecords(t, "inc-count", sid, recs)
	copyPath, _, _ := flowCopyPaths("inc-count", "claude", sid)

	added, off, reset := countUserTurnsFrom(copyPath, ref, 0)
	if added != 2 || reset {
		t.Fatalf("first pass: added=%d reset=%v, want 2,false", added, reset)
	}
	if off <= 0 {
		t.Fatalf("offset did not advance: %d", off)
	}

	// No new bytes → nothing more to count, offset unchanged.
	if a2, o2, _ := countUserTurnsFrom(copyPath, ref, off); a2 != 0 || o2 != off {
		t.Fatalf("no-change pass: added=%d off=%d, want 0,%d", a2, o2, off)
	}

	// Append two more user turns; the incremental pass reads only those.
	recs = append(recs,
		harness.NormalizedRecord{Role: harness.RoleUser, TS: after, Text: "u3"},
		harness.NormalizedRecord{Role: harness.RoleUser, TS: after, Injected: true, Text: "wake"},
		harness.NormalizedRecord{Role: harness.RoleUser, TS: after, Text: "u4"},
	)
	writeCopyRecords(t, "inc-count", sid, recs)
	a3, o3, _ := countUserTurnsFrom(copyPath, ref, off)
	if a3 != 2 { // u3 and u4; the injected one is skipped
		t.Fatalf("incremental pass: added=%d, want 2", a3)
	}
	if o3 <= off {
		t.Fatalf("offset did not advance on append: %d <= %d", o3, off)
	}

	// A smaller copy than the saved offset (rebuild) signals a reset.
	if _, _, reset := countUserTurnsFrom(copyPath, ref, o3+1_000_000); !reset {
		t.Fatal("offset past EOF should signal reset")
	}
}

// ---------- primary (transcript copy) path end to end ----------

func TestNoteNudgePrimaryCopyPath(t *testing.T) {
	setupFlowRoot(t) // copy enabled by default
	const sid = "99999999-9999-4999-8999-999999999999"
	bindNudgeTask(t, "nudge-copy", sid, past(time.Hour))

	// Pre-seed 14 genuine user records (plus noise the counter must ignore).
	now := time.Now().Format(time.RFC3339)
	var recs []harness.NormalizedRecord
	for i := 0; i < 14; i++ {
		recs = append(recs, harness.NormalizedRecord{Role: harness.RoleUser, TS: now, Text: fmt.Sprintf("u%d", i)})
	}
	recs = append(recs,
		harness.NormalizedRecord{Role: harness.RoleUser, TS: now, Injected: true, Text: "wake"},
		harness.NormalizedRecord{Role: harness.RoleAssistant, TS: now, Text: "a"},
	)
	writeCopyRecords(t, "nudge-copy", sid, recs)

	// The hook's capture appends this 15th genuine prompt, crossing base.
	ctx := upsNudge(t, sid, "fifteenth human prompt")
	if !nudged(ctx, "nudge-copy") {
		t.Fatalf("expected a nudge once the copy reaches 15 user turns; got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "flow: 15 turns since the last progress note on nudge-copy") {
		t.Errorf("nudge count wrong; got:\n%s", ctx)
	}
}

// ---------- the hook never errors ----------

func TestNoteNudgeHookNeverErrors(t *testing.T) {
	setupFlowRoot(t)
	// readHookPayload leaves the session id in package globals; don't leak
	// it into a later test's currentSessionID().
	t.Cleanup(func() { hookSessionID, hookPrompt, hookPromptKnown = "", "", false })
	const sid = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	bindNudgeTask(t, "nudge-robust", sid, past(time.Hour))
	// Corrupt settings.json — the loader must treat it as defaults, and the
	// hook must still exit 0.
	root, _ := flowRoot()
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte("{ this is not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"session_id": sid, "prompt": "hi"})
	withHookStdin(t, string(payload), func() {
		_ = captureStdout(t, func() {
			if rc := cmdHookUserPromptSubmit(nil); rc != 0 {
				t.Fatalf("hook rc=%d with corrupt settings", rc)
			}
		})
	})
}

// ---------- latency: the nudge must keep the hook fast (< 20 ms) ----------

func TestNoteNudgeLatency(t *testing.T) {
	setupFlowRoot(t)
	const sid = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	bindNudgeTask(t, "nudge-lat", sid, past(time.Hour))

	// A realistic-to-large copy: ~3000 user turns interleaved with assistant
	// and tool records carrying non-trivial text.
	now := time.Now().Format(time.RFC3339)
	body := strings.Repeat("x", 400)
	var recs []harness.NormalizedRecord
	for i := 0; i < 3000; i++ {
		recs = append(recs,
			harness.NormalizedRecord{Role: harness.RoleUser, TS: now, Text: "u"},
			harness.NormalizedRecord{Role: harness.RoleAssistant, TS: now, Text: body},
			harness.NormalizedRecord{Role: harness.RoleToolResult, TS: now, Text: body},
		)
	}
	writeCopyRecords(t, "nudge-lat", sid, recs)

	// Drive noteNudgeContext directly (the capture+bus work is measured
	// elsewhere). Make the package see a human prompt.
	hookPromptKnown, hookPrompt = true, "a real human prompt"
	t.Cleanup(func() { hookSessionID, hookPrompt, hookPromptKnown = "", "", false })
	db := openFlowDB(t)
	task, err := flowdb.TaskBySessionID(db, sid)
	if err != nil || task == nil {
		t.Fatalf("load task: %v", err)
	}

	const iters = 50
	start := time.Now()
	for i := 0; i < iters; i++ {
		_ = noteNudgeContext(task)
	}
	per := time.Since(start) / iters
	t.Logf("note-nudge latency: %v/call over a %d-turn copy", per, len(recs)/3)
	if per > 20*time.Millisecond {
		t.Errorf("note-nudge too slow: %v/call (> 20ms target)", per)
	}
}
