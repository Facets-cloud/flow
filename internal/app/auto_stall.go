package app

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness"
)

// Stall watchdog for autonomous runs. A headless child can hang on a tool
// call with its process still alive (observed: ~18 min on a browser call).
// Nothing reports that: the dead notice fires only when the process exits.
// The watchdog watches for silence instead. Silence means no movement in the
// run's log, its session transcript, or its start time. It only reports;
// it never kills the run.

// autoStallDefault is the silence that counts as a stall when
// FLOW_AUTO_STALL_MINUTES is unset or not a positive integer.
const autoStallDefault = 10 * time.Minute

// autoInFlightCeiling is the silence tolerated while a tool call or background
// job is in flight. A
// call writes nothing until it returns, so this is the longest one call may run
// before it counts as hung. FLOW_AUTO_STALL_MINUTES does not change it.
const autoInFlightCeiling = 60 * time.Minute

// autoLogTail bounds how much of a run log is scanned for tool calls in flight.
// An in-flight call's start line is the last write before it returns, so the
// tail always holds it.
const autoLogTail = 1 << 20

// autoStallEnvVar overrides the stall threshold, in whole minutes.
const autoStallEnvVar = "FLOW_AUTO_STALL_MINUTES"

// autoStallInterval is how often the supervisor samples the run's activity.
var autoStallInterval = 30 * time.Second

// maxStallMinutes is the largest whole-minute count that still fits a Duration.
// A larger override is clamped to it, so it cannot wrap negative.
const maxStallMinutes = int64(math.MaxInt64 / int64(time.Minute))

func autoStallThreshold() time.Duration {
	n, err := strconv.ParseInt(os.Getenv(autoStallEnvVar), 10, 64)
	// A positive overflow comes back saturated with ErrRange: clamp it, do not default.
	if (err != nil && !errors.Is(err, strconv.ErrRange)) || n <= 0 {
		return autoStallDefault
	}
	n = min(n, maxStallMinutes)
	return time.Duration(n) * time.Minute
}

// stallWatch decides when a run has stalled. A run is stalled once it has been
// silent for threshold. It fires once per episode and re-arms only when
// activity moves past the last one seen.
type stallWatch struct {
	threshold time.Duration
	ceiling   time.Duration // silence tolerated while a tool call is in flight
	seen      time.Time     // newest activity observed; starts at the run's start
	notified  bool
}

// observe folds in the newest activity seen at now and reports whether a
// stall notice is due. A zero or older activity time is no news. A tool call in
// flight is judged against the ceiling, not the threshold.
func (w *stallWatch) observe(activity time.Time, inFlight bool, now time.Time) bool {
	if activity.After(w.seen) {
		w.seen, w.notified = activity, false
		return false
	}
	limit := w.threshold
	if inFlight {
		limit = w.ceiling
	}
	if w.notified || now.Sub(w.seen) < limit {
		return false
	}
	w.notified = true
	return true
}

// startStallWatchdog samples activity every interval until the returned stop
// is called. onStall runs once per stall episode, with the silence so far. It
// returns false when the notice was not delivered; the episode then stays open
// and onStall is retried on a later sample. stop returns only after the sampler
// has exited, so no notice can follow it. The supervisor calls stop before it finalizes the run.
// Silence counts from the newest activity already on record (the same reading
// `flow list` uses), so a run that was silent before the sampler started is
// reported on the first stall check, not a full threshold later.
func startStallWatchdog(threshold, ceiling, interval time.Duration, activity func() (time.Time, bool), onStall func(idle time.Duration) bool) (stop func()) {
	quit := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		seen, _ := activity()
		if seen.IsZero() {
			seen = time.Now()
		}
		w := stallWatch{threshold: threshold, ceiling: ceiling, seen: seen}
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-quit:
				return
			case now := <-tick.C:
				select {
				case <-quit:
					return
				default:
				}
				at, inFlight := activity()
				if w.observe(at, inFlight, now) && !onStall(now.Sub(w.seen)) {
					w.notified = false
				}
			}
		}
	}()
	return func() {
		close(quit)
		<-finished
	}
}

// autoRunActivity is the newest sign of life of a run: its start, its log
// file, or its session transcript, whichever moved last. The transcript is
// what moves for a headless claude or prx run, whose stdout stays silent until
// the final message. A file that cannot be stat'ed contributes nothing. inFlight
// reports that the run log ends inside a tool call that has not returned.
func autoRunActivity(t *flowdb.Task) (last time.Time, inFlight bool) {
	consider := func(ts time.Time) {
		if ts.After(last) {
			last = ts
		}
	}
	if t.AutoRunStarted.Valid {
		if ts, err := time.Parse(time.RFC3339, t.AutoRunStarted.String); err == nil {
			consider(ts)
		}
	}
	consider(fileMTime(t.AutoRunLog.String))
	inFlight = autoRunToolInFlight(t.AutoRunLog.String)
	if t.SessionID.Valid && t.SessionID.String != "" {
		if h, err := harnessForSpawn(t); err == nil {
			if loc, ok := h.(harness.TranscriptLocator); ok {
				path := loc.TranscriptPath(t.WorkDir, t.SessionID.String)
				consider(fileMTime(path))
				inFlight = inFlight || autoRunJobOpen(path)
			}
		}
	}
	return last, inFlight
}

// autoRunJobOpen reports whether a prx transcript holds a background job that
// has started and not yet finished or been killed. The run log cannot pair a
// job's start with its end: the start line cuts the job id short and the
// completion is never logged, so the transcript's job rows are the source.
// Claude and Codex transcripts carry no such rows and report false.
func autoRunJobOpen(path string) bool {
	if path == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	open := map[string]struct{}{}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			// Longer than the buffer: never a job row, so skip to its end.
			for err == bufio.ErrBufferFull {
				_, err = r.ReadSlice('\n')
			}
		} else if bytes.Contains(line, []byte(`"type":"job"`)) {
			applyJobRow(open, line)
		}
		if err != nil {
			break
		}
	}
	return len(open) > 0
}

// applyJobRow records one transcript job row: a "started" row opens its job,
// a "finished" or "killed" row closes it. jobId carries a "@<session>" suffix
// that is not part of the job's identity.
func applyJobRow(open map[string]struct{}, line []byte) {
	var row struct {
		Type  string `json:"type"`
		JobID string `json:"jobId"`
		State string `json:"state"`
	}
	if json.Unmarshal(line, &row) != nil || row.Type != "job" {
		return
	}
	id, _, _ := strings.Cut(row.JobID, "@")
	if id == "" {
		return
	}
	switch row.State {
	case "started":
		open[id] = struct{}{}
	case "finished", "killed":
		delete(open, id)
	}
}

// autoRunToolInFlight reports whether the newest tool call in the run log has
// started and not returned. The prx host writes `[tool] <name>(<args>` when a
// call starts and `[tool] <name> -> <result>` when it returns. The start can
// follow assistant text on the same line, so every marker is matched. Claude and
// Codex auto-run logs carry no such lines and never report in flight.
func autoRunToolInFlight(path string) bool {
	buf, err := readLogTail(path, autoLogTail)
	if err != nil {
		return false
	}
	const marker = "[tool] "
	pending := 0
	for {
		i := bytes.Index(buf, []byte(marker))
		if i < 0 {
			return pending > 0
		}
		buf = buf[i+len(marker):]
		n := 0
		for n < len(buf) && isToolNameByte(buf[n]) {
			n++
		}
		switch {
		case n == 0:
		case n < len(buf) && buf[n] == '(':
			pending++
		case bytes.HasPrefix(buf[n:], []byte(" -> ")):
			if pending > 0 {
				pending--
			}
		}
	}
}

func isToolNameByte(c byte) bool {
	return c == '_' || c == '-' || c == '.' || ('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// readLogTail returns up to the last n bytes of path, starting after a line
// break when it cut one.
func readLogTail(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off := max(fi.Size()-n, 0)
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	buf, err := io.ReadAll(f)
	if i := bytes.IndexByte(buf, '\n'); off > 0 && i >= 0 {
		buf = buf[i+1:]
	}
	return buf, err
}

// fileMTime is the mtime of path, or the zero time when path is empty or
// missing.
func fileMTime(path string) time.Time {
	if path == "" {
		return time.Time{}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// autoRunStalledFor reports how long a running run has been silent and whether
// that is past the stall threshold. Only a 'running' run can be stalled: a
// finished or dead run is never reported as stalled.
func autoRunStalledFor(t *flowdb.Task, now time.Time) (time.Duration, bool) {
	if !t.AutoRunStatus.Valid || t.AutoRunStatus.String != "running" {
		return 0, false
	}
	last, inFlight := autoRunActivity(t)
	if last.IsZero() {
		// No start time and no file to read: nothing says the run is silent.
		return 0, false
	}
	idle := now.Sub(last)
	limit := autoStallThreshold()
	if inFlight {
		limit = autoInFlightCeiling
	}
	return idle, idle >= limit
}

// lastLogLine returns the last non-empty line of the run log, trimmed to a
// length that keeps the parent notice near one line. "" when the log is absent or empty.
func lastLogLine(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const tail = 4096
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	off := fi.Size() - tail
	if off < 0 {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return ""
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\r\n \t"), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	if r := []rune(line); len(r) > 60 {
		line = string(r[:57]) + "..."
	}
	return line
}

// notifyParentOfStalledChild tells the parent that a run has gone silent and
// reports whether the notice is settled. A parentless run is not messaged: the
// human hears only at done or blocked, and a stall is visible in `flow list`
// and `flow show`, so that counts as settled.
func notifyParentOfStalledChild(db *sql.DB, child *flowdb.Task, idle time.Duration) bool {
	if !child.ParentSlug.Valid || child.ParentSlug.String == "" {
		return true
	}
	// A transient lookup failure is not a closed parent: report the notice as
	// not stored so the watchdog retries, instead of sending a human FYI now.
	if _, err := flowdb.GetTask(db, child.ParentSlug.String); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false
	}
	last := "no log output"
	if l := lastLogLine(child.AutoRunLog.String); l != "" {
		last = "last log: " + l
	}
	body := fmt.Sprintf("auto child %s stalled: no progress for %d min; %s; check `flow show task %s`",
		child.Slug, int(idle.Minutes()), last, child.Slug)
	closedBody := fmt.Sprintf("FYI: auto child %s of closed task %s stalled: no progress for %d min; check `flow show task %s`",
		child.Slug, child.ParentSlug.String, int(idle.Minutes()), child.Slug)
	return notifyParentOfChild(db, child, body, closedBody)
}
