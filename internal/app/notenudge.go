package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flow/internal/flowdb"
	"flow/internal/harness"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The progress-note nudge. flow's durable value comes from the progress
// notes (updates/*.md) a session leaves behind, but long sessions often run
// many turns without one. This nudge watches how many user turns have
// elapsed since the bound task's last note and, once past a threshold,
// injects ONE extra line of UserPromptSubmit context suggesting the agent
// offer to save a note. It fires again at each further compounding step
// (default +20%): 15, 18, 22, 26, 32, … Writing a new note resets the count.
//
// Design constraints (all load-bearing):
//   - Rides the EXISTING UserPromptSubmit hook — no new hook, no changed
//     hook command string.
//   - Never blocks or fails the hook: every path swallows its errors, and
//     noteNudgeContextSafe recovers from any panic.
//   - Bound, human-driven sessions only. Auto runs (FLOW_AUTO_RUN) and
//     unbound sessions never see it; injected prompts (wake pushes, --with,
//     bootstrap) never count or fire.
//   - Fires once per threshold crossing, not every turn.
//
// Turn counting prefers flow's transcript copy (genuine, non-injected user
// records whose timestamp is after the reference instant). When the copy is
// disabled, it falls back to a tiny per-session counter persisted in the
// state sidecar.

// noteNudgeState is the per-session sidecar that makes the nudge fire once
// per crossing and reset when a new note is written. It lives next to the
// transcript copy's offset sidecar, under tasks/<slug>/transcripts/.
type noteNudgeState struct {
	// Reference is the RFC3339Nano reference instant the counts are measured
	// from (the newest updates/*.md mtime, else the session start). When it
	// changes — a new note was written — LastFired and Fallback reset to 0.
	Reference string `json:"reference"`
	// LastFired is the highest threshold already nudged at, so each crossing
	// fires exactly once.
	LastFired int `json:"last_fired"`
	// Fallback is the user-turn counter used ONLY when the transcript copy
	// is off: incremented once per human prompt since Reference.
	Fallback int `json:"fallback"`
	// Offset and Count make the primary (copy) path incremental: Offset is
	// the byte of the copy the counter has consumed, and Count is the running
	// count of qualifying user turns since Reference. Each prompt reads ONLY
	// the bytes appended since Offset rather than rescanning the whole copy.
	// Both reset to 0 with Reference (a new note) and self-heal if the copy is
	// rebuilt smaller than Offset.
	Offset int64 `json:"offset"`
	Count  int   `json:"count"`
}

// noteNudgeConfig reads the nudge settings with their documented defaults.
// A per-task enabled override (nudge.notes.tasks.<slug>.enabled) wins over
// the global nudge.notes.enabled.
func noteNudgeConfig(slug string) (enabled bool, base int, step float64) {
	m := loadSettings()
	enabled = settingBool(m, "nudge.notes.enabled", true)
	if v, ok := settingGet(m, "nudge.notes.tasks."+slug+".enabled"); ok {
		enabled = boolish(v, enabled)
	}
	base = settingInt(m, "nudge.notes.base_turns", 15)
	step = settingFloat(m, "nudge.notes.step", 0.2)
	return enabled, base, step
}

// noteNudgeThreshold returns the highest nudge threshold at or below count,
// and whether count has reached the first (base) threshold at all. The
// thresholds are base, then ceil(base*(1+step)^k) for k=1,2,… — each forced
// strictly above the previous so a tiny step can never stall the loop or
// repeat a value. A non-positive step yields a single threshold at base
// (one nudge, no further steps).
func noteNudgeThreshold(count, base int, step float64) (int, bool) {
	if base < 1 {
		base = 1
	}
	if count < base {
		return 0, false
	}
	if step <= 0 {
		return base, true
	}
	prev := base
	factor := 1.0
	for {
		factor *= 1 + step
		next := int(math.Ceil(float64(base) * factor))
		if next <= prev {
			next = prev + 1
		}
		if next > count {
			return prev, true
		}
		prev = next
	}
}

// noteNudgeStatePath is tasks/<slug>/transcripts/<harness>-<sid>.note-nudge.json.
// It shares the transcripts dir with the copy and its offset sidecar, so it
// never surfaces under `flow show`'s `other:` (that lists top-level .md only).
func noteNudgeStatePath(t *flowdb.Task) (string, error) {
	dir, err := taskTranscriptsDir(t.Slug)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, copyBaseName(taskHarnessName(t), t.SessionID.String)+".note-nudge.json"), nil
}

func loadNoteNudgeState(path string) *noteNudgeState {
	b, err := os.ReadFile(path)
	if err != nil {
		return &noteNudgeState{}
	}
	var st noteNudgeState
	if json.Unmarshal(b, &st) != nil {
		return &noteNudgeState{}
	}
	return &st
}

func saveNoteNudgeState(path string, st *noteNudgeState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
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

// noteNudgeReference is the instant the turn count is measured from: the
// newest progress-note mtime for the task, or — when there are no notes —
// the session start. Zero time (count everything) if neither is available.
func noteNudgeReference(t *flowdb.Task) time.Time {
	if mt, ok := newestUpdateMtime(t.Slug); ok {
		return mt
	}
	if t.SessionStarted.Valid && t.SessionStarted.String != "" {
		if ts, err := time.Parse(time.RFC3339, t.SessionStarted.String); err == nil {
			return ts
		}
	}
	return time.Time{}
}

// newestUpdateMtime returns the modification time of the most recently
// written updates/*.md file for the task, and whether any exists.
func newestUpdateMtime(slug string) (time.Time, bool) {
	root, err := flowRoot()
	if err != nil {
		return time.Time{}, false
	}
	entries, err := os.ReadDir(filepath.Join(root, "tasks", slug, "updates"))
	if err != nil {
		return time.Time{}, false
	}
	var newest time.Time
	found := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !found || info.ModTime().After(newest) {
			newest = info.ModTime()
			found = true
		}
	}
	return newest, found
}

// roleUserMarker is the exact substring a user record's JSON carries. It is
// a cheap pre-filter: assistant / tool_result lines (which hold the bulk of
// a transcript's bytes) are skipped without a JSON parse, keeping the count
// O(user-turns) rather than O(transcript-size). A false positive (the
// substring inside some tool output) is still fully parsed and rejected by
// the role check below, so the filter never miscounts.
var roleUserMarker = []byte(`"role":"user"`)

// minRecord decodes only the fields countUserTurnsSince needs, so a 400 KB
// tool result is scanned but never allocated into a string.
type minRecord struct {
	Role     string `json:"role"`
	TS       string `json:"ts"`
	Injected bool   `json:"injected"`
}

// countUserTurnsSince counts genuine (non-injected) user records in the
// flow transcript copy whose timestamp is after since, scanning the WHOLE
// copy. Retained for callers that want a one-shot count; the nudge itself
// uses the incremental countUserTurnsFrom.
func countUserTurnsSince(copyPath string, since time.Time) int {
	n, _, _ := countUserTurnsFrom(copyPath, since, 0)
	return n
}

// countUserTurnsFrom counts genuine (non-injected) user records whose
// timestamp is after since, reading the copy ONLY from byte fromOffset
// through the last complete (newline-terminated) line — so a per-prompt call
// touches just the bytes appended since the previous one, not the whole copy
// (point 6 of the review: the nudge must not rescan a large copy every
// prompt). It returns the count found in that window and the new offset (just
// past the last complete line). reset is true when fromOffset is past EOF
// (the copy was rebuilt/truncated), signalling the caller to recount from 0.
// A missing copy or read error yields (0, fromOffset, false). An unparseable
// timestamp counts the record (never lose a turn to a formatting quirk).
func countUserTurnsFrom(copyPath string, since time.Time, fromOffset int64) (added int, newOffset int64, reset bool) {
	f, err := os.Open(copyPath)
	if err != nil {
		return 0, fromOffset, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, fromOffset, false
	}
	if fromOffset > fi.Size() {
		return 0, 0, true // copy shrank/rebuilt — caller recounts from 0
	}
	if fromOffset > 0 {
		if _, err := f.Seek(fromOffset, 0); err != nil {
			return 0, fromOffset, false
		}
	}
	br := bufio.NewReaderSize(f, 64*1024)
	consumed := fromOffset
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			break // no trailing newline: leave the partial line for next time
		}
		consumed += int64(len(line))
		if !bytes.Contains(line, roleUserMarker) {
			continue
		}
		var r minRecord
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		if r.Role != harness.RoleUser || r.Injected {
			continue
		}
		if ts, perr := time.Parse(time.RFC3339, r.TS); perr != nil || ts.After(since) {
			added++
		}
	}
	return added, consumed, false
}

// noteNudgeContext returns the one-line nudge to append to the bound
// session's UserPromptSubmit context, or "" when nothing should fire. It is
// gated to bound, human-driven sessions; see the file comment. The caller
// is cmdHookUserPromptSubmit (via noteNudgeContextSafe).
func noteNudgeContext(t *flowdb.Task) string {
	if t == nil || isAutoRunSession(t) {
		return ""
	}
	if !t.SessionID.Valid || t.SessionID.String == "" {
		return ""
	}
	// Only genuine human prompts advance or fire the nudge; injected
	// payloads never count.
	if !currentPromptIsHuman() {
		return ""
	}
	enabled, base, step := noteNudgeConfig(t.Slug)
	if !enabled {
		return ""
	}
	path, err := noteNudgeStatePath(t)
	if err != nil {
		return ""
	}
	st := loadNoteNudgeState(path)

	ref := noteNudgeReference(t)
	refKey := ref.Format(time.RFC3339Nano)
	if st.Reference != refKey {
		// A new note (or the first prompt): reset every counter.
		st.Reference = refKey
		st.LastFired = 0
		st.Fallback = 0
		st.Offset = 0
		st.Count = 0
	}

	var count int
	if transcriptsCopyEnabled(t.Slug) {
		// Primary path: the UserPromptSubmit capture already appended this
		// turn's prompt to the copy, so counting it includes the current turn.
		// Incremental: read only the bytes appended since the last prompt.
		if copyPath, _, perr := flowCopyPaths(t.Slug, taskHarnessName(t), t.SessionID.String); perr == nil {
			added, newOff, reset := countUserTurnsFrom(copyPath, ref, st.Offset)
			if reset {
				st.Count, st.Offset = 0, 0
				added, newOff, _ = countUserTurnsFrom(copyPath, ref, 0)
			}
			st.Count += added
			st.Offset = newOff
			count = st.Count
		}
	} else {
		// Fallback path (copy disabled): self-count one turn per human prompt.
		st.Fallback++
		count = st.Fallback
	}

	threshold, ok := noteNudgeThreshold(count, base, step)
	fire := ok && threshold > st.LastFired
	if fire {
		st.LastFired = threshold
	}
	_ = saveNoteNudgeState(path, st)
	if !fire {
		return ""
	}
	return fmt.Sprintf(
		" flow: %d turns since the last progress note on %s. If there is "+
			"meaningful progress, offer to save a note (skill §4.5).",
		count, t.Slug,
	)
}

// noteNudgeContextSafe wraps noteNudgeContext so a bug can never block or
// fail the UserPromptSubmit hook — any panic yields no nudge.
func noteNudgeContextSafe(t *flowdb.Task) (ctx string) {
	defer func() {
		if r := recover(); r != nil {
			logTranscriptErr("note-nudge panic: %v", r)
			ctx = ""
		}
	}()
	return noteNudgeContext(t)
}
