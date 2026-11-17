package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/match"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// This file is flow's harness-agnostic transcript store. Harnesses delete
// (or compress/rotate) their own session transcripts on their own
// schedule, so recall — which learns from the user's own past prompts —
// loses most history over time. Flow therefore keeps its OWN normalized
// copy of each task's transcript, one documented schema for every harness,
// under $FLOW_ROOT/tasks/<slug>/transcripts/<harness>-<session-id>.jsonl.
//
// Capture rides the EXISTING hooks (no new hook, no changed command
// string): the Stop hook appends each turn's new records incrementally
// from a saved byte offset, and the UserPromptSubmit hook may append the
// just-typed prompt immediately (deduped against the Stop capture). A
// one-time `flow transcript backfill` seeds copies from transcripts that
// still exist. Everything here is append-only and must never fail or block
// the harness — capture errors are swallowed and logged, never surfaced.
//
// The on-disk schema (one NormalizedRecord per line) is documented in
// docs/transcripts.md. Opt out with `flow config set transcripts.copy off`
// (global) or `transcripts.tasks.<slug>.copy off` (per task); purge copies
// with `flow transcript purge`.

// captureState is the per-copy sidecar (<harness>-<sid>.offset.json). It
// lets each capture read ONLY the bytes that arrived since last time, and
// detect truncation/rotation of the source.
type captureState struct {
	SourcePath  string `json:"source_path"`  // the harness transcript last read
	SourceSize  int64  `json:"source_size"`  // its size at that read
	SourceInode uint64 `json:"source_inode"` // its inode — a new inode == rotation
	Offset      int64  `json:"offset"`       // bytes of the source consumed so far
	Turn        int    `json:"turn"`         // last turn number assigned (user-prompt count)
	Records     int    `json:"records"`      // normalized records written to the copy
	// PendingPrompt is a prompt the UserPromptSubmit hook wrote to the copy
	// ahead of the Stop capture; the next Stop capture skips the matching
	// source user-record once so it is not duplicated.
	PendingPrompt string `json:"pending_prompt,omitempty"`
}

// taskTranscriptsDir is $FLOW_ROOT/tasks/<slug>/transcripts.
func taskTranscriptsDir(slug string) (string, error) {
	root, err := flowRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "tasks", slug, "transcripts"), nil
}

// copyBaseName is "<harness>-<sid>" — the stem shared by the copy and its
// sidecar.
func copyBaseName(harnessName, sid string) string {
	if harnessName == "" {
		harnessName = string(harness.NameClaude)
	}
	return harnessName + "-" + sid
}

func flowCopyPaths(slug, harnessName, sid string) (copyPath, statePath string, err error) {
	dir, err := taskTranscriptsDir(slug)
	if err != nil {
		return "", "", err
	}
	base := filepath.Join(dir, copyBaseName(harnessName, sid))
	return base + ".jsonl", base + ".offset.json", nil
}

func loadCaptureState(path string) *captureState {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var st captureState
	if json.Unmarshal(b, &st) != nil {
		return nil
	}
	return &st
}

func saveCaptureState(path string, st *captureState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// taskHarnessName returns the task's stored harness name, defaulting to
// "claude" for NULL/empty (the back-compat convention).
func taskHarnessName(t *flowdb.Task) string {
	if t != nil && t.Harness.Valid && t.Harness.String != "" {
		return t.Harness.String
	}
	return string(harness.NameClaude)
}

// captureTranscript appends every normalized record that arrived in the
// task's harness transcript since the last capture. It is idempotent, does
// the incremental offset read on the normal path, and rebuilds the whole
// copy on rotation/truncation or a compressed source. Returns the number of
// records and source bytes newly consumed.
func captureTranscript(t *flowdb.Task) (addedRecords int, addedBytes int64, err error) {
	if t == nil || !t.SessionID.Valid || t.SessionID.String == "" {
		return 0, 0, nil
	}
	if !transcriptsCopyEnabled(t.Slug) {
		return 0, 0, nil
	}
	sid := t.SessionID.String
	hName := taskHarnessName(t)
	h, err := harnessForTask(t)
	if err != nil {
		return 0, 0, err
	}
	pather, ok := h.(harness.TranscriptPather)
	if !ok {
		return 0, 0, nil // harness can't locate its transcript — nothing to copy
	}
	norm, ok := h.(harness.TranscriptNormalizer)
	if !ok {
		return 0, 0, nil
	}
	sourcePath, err := pather.TranscriptPath(t.WorkDir, sid)
	if err != nil {
		return 0, 0, nil // no source transcript yet (or already deleted) — not an error
	}
	fi, err := os.Stat(sourcePath)
	if err != nil {
		return 0, 0, nil
	}
	size, inode := fi.Size(), fileInode(fi)

	copyPath, statePath, err := flowCopyPaths(t.Slug, hName, sid)
	if err != nil {
		return 0, 0, err
	}
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o755); err != nil {
		return 0, 0, err
	}
	st := loadCaptureState(statePath)

	// Nothing changed since the last read.
	if st != nil && st.SourcePath == sourcePath && st.SourceInode == inode && st.SourceSize == size {
		return 0, 0, nil
	}

	// The first capture of a very large source must never read it from byte 0
	// inside the hook: a live task whose harness transcript is already
	// hundreds of MB (e.g. the first Stop after a flow upgrade) would stall
	// the turn for seconds. Seed the copy at EOF instead — an empty copy whose
	// saved offset is the current tail — so the hook stays O(1); later
	// captures append only the bytes that arrive after the seed, and
	// `flow transcript backfill` re-reads the whole source on demand to
	// recover the pre-seed history. (Only the fresh-copy case: a rotation or
	// an existing small copy still takes the normal rebuild/incremental path.)
	if st == nil && size > transcriptHookEOFThreshold() {
		n, err := seedFlowCopyAtEOF(sourcePath, copyPath, statePath, size, inode)
		return n, size, err
	}

	isGz := strings.HasSuffix(sourcePath, ".gz")
	rebuild := isGz || st == nil || st.SourcePath != sourcePath || st.SourceInode != inode || st.Offset > size

	if rebuild {
		n, err := rebuildFlowCopy(norm, hName, sid, sourcePath, copyPath, statePath, size, inode)
		return n, size, err
	}
	return captureIncremental(norm, hName, sid, sourcePath, copyPath, statePath, st, size, inode)
}

// rebuildFlowCopy re-reads the whole source and rewrites the copy from
// scratch. Used for a fresh copy, after rotation/truncation, and for
// compressed sources (where incremental byte offsets are meaningless). The
// write is atomic (temp + rename) so a mid-rebuild crash never leaves a
// half-written copy.
func rebuildFlowCopy(norm harness.TranscriptNormalizer, hName, sid, sourcePath, copyPath, statePath string, size int64, inode uint64) (int, error) {
	isGz := strings.HasSuffix(sourcePath, ".gz")
	var recs []harness.NormalizedRecord
	// offset is where the next incremental read should resume: the byte
	// just past the last COMPLETE line. For a compressed source there is no
	// meaningful byte offset (the size/inode guard detects any change), so
	// it is left at the file size.
	offset := size
	if isGz {
		f, err := os.Open(sourcePath)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		if sn, ok := norm.(harness.StreamNormalizer); ok {
			recs, err = sn.NormalizeStream(f)
		} else {
			recs, err = normalizeByLine(norm, f)
		}
		if err != nil {
			return 0, err
		}
	} else {
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return 0, err
		}
		lastNL := bytes.LastIndexByte(data, '\n')
		if lastNL < 0 {
			offset = 0 // no complete line yet
		} else {
			offset = int64(lastNL + 1)
			for _, line := range bytes.Split(data[:lastNL+1], []byte("\n")) {
				if len(line) == 0 {
					continue
				}
				lineRecs, _ := norm.NormalizeLine(line)
				recs = append(recs, lineRecs...)
			}
		}
	}
	stampRecords(recs, hName, sid)
	turn := assignTurns(recs, 0)

	tmp := copyPath + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	bw := bufio.NewWriter(out)
	if err := writeRecords(bw, recs); err != nil {
		out.Close()
		os.Remove(tmp)
		return 0, err
	}
	if err := bw.Flush(); err != nil {
		out.Close()
		os.Remove(tmp)
		return 0, err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, copyPath); err != nil {
		return 0, err
	}
	st := &captureState{
		SourcePath: sourcePath, SourceSize: size, SourceInode: inode,
		Offset: offset, Turn: turn, Records: len(recs),
	}
	if err := saveCaptureState(statePath, st); err != nil {
		return len(recs), err
	}
	return len(recs), nil
}

// transcriptHookEOFThreshold is the source size above which the FIRST hook
// capture seeds the copy at EOF instead of reading the whole file (point 3:
// the Stop/UserPromptSubmit capture must never read a large source from byte
// 0). Default 8 MiB; overridable via $FLOW_TRANSCRIPT_HOOK_EOF_BYTES (an env
// var, so it stays out of settings.json and its validation surface) for tests
// and tuning.
func transcriptHookEOFThreshold() int64 {
	const def = 8 << 20 // 8 MiB
	if v := os.Getenv("FLOW_TRANSCRIPT_HOOK_EOF_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// seedFlowCopyAtEOF creates an empty copy and records a capture state whose
// offset is the source's current tail, so subsequent captures append only
// the bytes that arrive afterwards. It reads only a small window at the end
// of the source to land the offset on a complete-line boundary (so the first
// incremental read does not begin mid-record); it never reads the whole file.
// The pre-seed history is left for `flow transcript backfill`.
func seedFlowCopyAtEOF(sourcePath, copyPath, statePath string, size int64, inode uint64) (int, error) {
	offset := eofLineBoundary(sourcePath, size)
	// Create (truncate) an empty copy so the copy "exists" for readers and
	// later appends open it in append mode.
	f, err := os.Create(copyPath)
	if err != nil {
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	st := &captureState{
		SourcePath: sourcePath, SourceSize: size, SourceInode: inode,
		Offset: offset, Turn: 0, Records: 0,
	}
	return 0, saveCaptureState(statePath, st)
}

// eofLineBoundary returns the byte offset just past the last newline at or
// before size, scanning only a bounded tail window (never the whole file).
// Falls back to size when no newline is found in the window or on any read
// error — one partial record may then be skipped by the next capture, which
// is harmless.
func eofLineBoundary(sourcePath string, size int64) int64 {
	const window = 1 << 20 // 1 MiB tail
	if size <= 0 {
		return 0
	}
	start := size - window
	if start < 0 {
		start = 0
	}
	f, err := os.Open(sourcePath)
	if err != nil {
		return size
	}
	defer f.Close()
	if _, err := f.Seek(start, 0); err != nil {
		return size
	}
	buf := make([]byte, size-start)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return size
	}
	buf = buf[:n]
	if idx := bytes.LastIndexByte(buf, '\n'); idx >= 0 {
		return start + int64(idx) + 1
	}
	return size
}

// captureIncremental reads the source from st.Offset to its current size,
// processing only complete (newline-terminated) lines, and appends their
// normalized records to the copy. A trailing partial line (the harness
// mid-write) is left for the next capture.
func captureIncremental(norm harness.TranscriptNormalizer, hName, sid, sourcePath, copyPath, statePath string, st *captureState, size int64, inode uint64) (int, int64, error) {
	f, err := os.Open(sourcePath)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	if _, err := f.Seek(st.Offset, 0); err != nil {
		return 0, 0, err
	}
	data := make([]byte, size-st.Offset)
	nRead, err := io.ReadFull(f, data)
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		err = nil // source shrank between stat and read — use what we got
	}
	if err != nil {
		return 0, 0, err
	}
	data = data[:nRead]
	// Only process through the last complete line.
	lastNL := bytes.LastIndexByte(data, '\n')
	if lastNL < 0 {
		return 0, 0, nil // no complete line yet
	}
	complete := data[:lastNL+1]

	var recs []harness.NormalizedRecord
	for _, line := range bytes.Split(complete, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		lineRecs, _ := norm.NormalizeLine(line)
		recs = append(recs, lineRecs...)
	}
	stampRecords(recs, hName, sid)

	// Dedupe the prompt the UserPromptSubmit hook already wrote: skip the
	// first source user-record matching the pending prompt (without
	// advancing the turn — the UPS append already counted it).
	recs, consumedPending := dropPendingPrompt(recs, st.PendingPrompt)
	startTurn := st.Turn
	turn := assignTurnsSkippingFirstPending(recs, startTurn)

	f2, err := os.OpenFile(copyPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, 0, err
	}
	bw := bufio.NewWriter(f2)
	if err := writeRecords(bw, recs); err != nil {
		f2.Close()
		return 0, 0, err
	}
	if err := bw.Flush(); err != nil {
		f2.Close()
		return 0, 0, err
	}
	if err := f2.Close(); err != nil {
		return 0, 0, err
	}

	consumed := int64(lastNL + 1)
	st.Offset += consumed
	st.SourceSize = size
	st.SourceInode = inode
	st.SourcePath = sourcePath
	st.Turn = turn
	st.Records += len(recs)
	if consumedPending {
		st.PendingPrompt = ""
	}
	if err := saveCaptureState(statePath, st); err != nil {
		return len(recs), consumed, err
	}
	return len(recs), consumed, nil
}

// normalizeByLine is the fallback for a normalizer that is not a
// StreamNormalizer: scan the reader line by line.
func normalizeByLine(norm harness.TranscriptNormalizer, f *os.File) ([]harness.NormalizedRecord, error) {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var out []harness.NormalizedRecord
	for sc.Scan() {
		recs, _ := norm.NormalizeLine(sc.Bytes())
		out = append(out, recs...)
	}
	return out, sc.Err()
}

// stampRecords fills the fields the normalizer leaves to the capture layer:
// harness, session id, a fallback timestamp, the tool-result cap, and the
// injected flag for hook/skill/system-reminder payloads.
func stampRecords(recs []harness.NormalizedRecord, hName, sid string) {
	now := time.Now().Format(time.RFC3339)
	maxTR := transcriptsMaxToolResultBytes()
	for i := range recs {
		recs[i].Harness = hName
		recs[i].SessionID = sid
		if recs[i].TS == "" {
			recs[i].TS = now
		}
		if !recs[i].Injected && isInjectedText(recs[i].Text) {
			recs[i].Injected = true
		}
		if maxTR > 0 && recs[i].Role == harness.RoleToolResult && len(recs[i].Text) > maxTR {
			recs[i].Text = recs[i].Text[:maxTR] + "…[truncated]"
		}
	}
}

// injectedMarkers are substrings that mark a record as a hook/skill/system
// payload rather than genuine user or model content. Flagged, never
// dropped, so a reader can filter them (recall drops them) without losing
// the audit trail.
var injectedMarkers = []string{
	harness.WakeMarker,      // "[flow-bus wake]"
	harness.InjectionMarker, // "[via flow do --with]"
	"<system-reminder>",
	"SessionStart hook additional context",
	"UserPromptSubmit hook additional context",
}

func isInjectedText(s string) bool {
	for _, m := range injectedMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// assignTurns numbers records by user-prompt count. A genuine (non-
// injected) user record opens a new turn; everything after it shares that
// turn. Records before the first user prompt get the starting turn.
func assignTurns(recs []harness.NormalizedRecord, turn int) int {
	for i := range recs {
		if recs[i].Role == harness.RoleUser && !recs[i].Injected {
			turn++
		}
		recs[i].Turn = turn
	}
	return turn
}

// assignTurnsSkippingFirstPending is assignTurns for the incremental path:
// identical numbering (the pending dedupe already removed the duplicate
// prompt, whose turn the UPS append counted).
func assignTurnsSkippingFirstPending(recs []harness.NormalizedRecord, turn int) int {
	return assignTurns(recs, turn)
}

// dropPendingPrompt removes the first user record whose text equals pending
// (the one the UPS hook already wrote). Returns the filtered slice and
// whether a match was consumed.
func dropPendingPrompt(recs []harness.NormalizedRecord, pending string) ([]harness.NormalizedRecord, bool) {
	if pending == "" {
		return recs, false
	}
	for i := range recs {
		if recs[i].Role == harness.RoleUser && !recs[i].Injected && recs[i].Text == pending {
			out := make([]harness.NormalizedRecord, 0, len(recs)-1)
			out = append(out, recs[:i]...)
			out = append(out, recs[i+1:]...)
			return out, true
		}
	}
	return recs, false
}

// writeRecords writes each record as one JSON line.
func writeRecords(bw *bufio.Writer, recs []harness.NormalizedRecord) error {
	enc := json.NewEncoder(bw)
	for i := range recs {
		if err := enc.Encode(&recs[i]); err != nil {
			return err
		}
	}
	return nil
}

// captureUserPrompt appends the just-submitted human prompt to the copy
// immediately (so the live turn's prompt is in the flow copy before the
// turn ends), and records it as pending so the Stop capture does not write
// it a second time. Injected prompts (wake pushes, `--with` injections) are
// left for the Stop capture to record (flagged); writing them here would
// double them.
func captureUserPrompt(t *flowdb.Task, prompt string) error {
	if t == nil || !t.SessionID.Valid || t.SessionID.String == "" || prompt == "" {
		return nil
	}
	if isInjectedText(prompt) {
		return nil
	}
	if !transcriptsCopyEnabled(t.Slug) {
		return nil
	}
	sid := t.SessionID.String
	hName := taskHarnessName(t)
	copyPath, statePath, err := flowCopyPaths(t.Slug, hName, sid)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o755); err != nil {
		return err
	}
	st := loadCaptureState(statePath)
	if st == nil {
		st = &captureState{}
	}
	// If a prompt is already pending, the previous turn never ran its Stop
	// capture (or it is still mid-turn). Don't stack a second ahead-write —
	// let the Stop capture catch up from the source instead.
	if st.PendingPrompt != "" {
		return nil
	}
	turn := st.Turn + 1
	rec := harness.NormalizedRecord{
		TS: time.Now().Format(time.RFC3339), Harness: hName, SessionID: sid,
		Turn: turn, Role: harness.RoleUser, Text: prompt,
	}
	f, err := os.OpenFile(copyPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(f)
	if err := writeRecords(bw, []harness.NormalizedRecord{rec}); err != nil {
		f.Close()
		return err
	}
	if err := bw.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	st.Turn = turn
	st.Records++
	st.PendingPrompt = prompt
	return saveCaptureState(statePath, st)
}

// ----- safe (hook-side) wrappers: never fail or block the harness -----

// captureTranscriptSafe runs captureTranscript and swallows+logs any error.
// Hooks call this: a capture failure must never surface to the user or
// block the turn.
func captureTranscriptSafe(t *flowdb.Task) {
	defer func() {
		if r := recover(); r != nil {
			logTranscriptErr("capture panic: %v", r)
		}
	}()
	if _, _, err := captureTranscript(t); err != nil {
		logTranscriptErr("capture %s: %v", slugOf(t), err)
	}
}

func captureUserPromptSafe(t *flowdb.Task, prompt string) {
	defer func() {
		if r := recover(); r != nil {
			logTranscriptErr("prompt capture panic: %v", r)
		}
	}()
	if err := captureUserPrompt(t, prompt); err != nil {
		logTranscriptErr("prompt capture %s: %v", slugOf(t), err)
	}
}

func slugOf(t *flowdb.Task) string {
	if t == nil {
		return "?"
	}
	return t.Slug
}

// logTranscriptErr appends a timestamped line to $FLOW_ROOT/transcripts.log.
// Best-effort: if even logging fails, give up silently (a hook must not
// error on a logging problem).
func logTranscriptErr(format string, args ...any) {
	root, err := flowRoot()
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(root, "transcripts.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// ----- readers -----

// readFlowCopyRecords decodes the flow copy at path into records. Missing
// file → (nil, nil). Malformed lines are skipped.
func readFlowCopyRecords(path string) ([]harness.NormalizedRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var out []harness.NormalizedRecord
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec harness.NormalizedRecord
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}

// flowCopyPathForTask returns the copy path for a task's bound session, and
// whether a copy exists on disk.
func flowCopyPathForTask(t *flowdb.Task) (string, bool) {
	if t == nil || !t.SessionID.Valid || t.SessionID.String == "" {
		return "", false
	}
	copyPath, _, err := flowCopyPaths(t.Slug, taskHarnessName(t), t.SessionID.String)
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(copyPath); err != nil {
		return "", false
	}
	return copyPath, true
}

// renderFlowCopy writes a human-readable rendering of the flow copy to w,
// matching the section shape the harness renderers use. compact omits tool
// results and assistant thinking is kept (the copy already has it). Used by
// `flow transcript` when the harness transcript is gone.
func renderFlowCopy(path string, compact bool, w *bytes.Buffer) error {
	recs, err := readFlowCopyRecords(path)
	if err != nil {
		return err
	}
	first := true
	section := func(label, body string) {
		if !first {
			fmt.Fprintln(w)
		}
		first = false
		fmt.Fprintln(w, label)
		fmt.Fprintln(w, body)
	}
	for _, r := range recs {
		switch r.Role {
		case harness.RoleUser:
			section("─── User ───", r.Text)
		case harness.RoleAssistant:
			section("─── Assistant ───", r.Text)
		case harness.RoleSystem:
			section("─── System ───", r.Text)
		case harness.RoleToolCall:
			section(fmt.Sprintf("─── Tool: %s ───", r.Tool), r.Text)
		case harness.RoleToolResult:
			if compact {
				continue
			}
			section("─── Result ───", r.Text)
		}
	}
	return nil
}

// flowCopyPrompts extracts cleaned user prompts from a task's flow copy —
// the recall prompt-loading seam. It reads only genuine (non-injected) user
// records, so it is immune to harness retention AND picks up a changed
// task's NEW prompts incrementally (the gap the cache previously had: it
// kept stale cached prompts until a full rebuild). Returns (nil, false)
// when no copy exists so callers fall back to the harness renderer.
func flowCopyPrompts(t *flowdb.Task) ([]string, bool) {
	copyPath, ok := flowCopyPathForTask(t)
	if !ok {
		return nil, false
	}
	recs, err := readFlowCopyRecords(copyPath)
	if err != nil {
		return nil, false
	}
	var out []string
	for _, r := range recs {
		if r.Role != harness.RoleUser || r.Injected {
			continue
		}
		if c, ok := match.CleanPrompt(r.Text); ok {
			out = append(out, c)
		}
	}
	return out, true
}
