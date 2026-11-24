package app

import (
	"database/sql"
	"fmt"
	"io"
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

// autoStallEnvVar overrides the stall threshold, in whole minutes.
const autoStallEnvVar = "FLOW_AUTO_STALL_MINUTES"

// autoStallInterval is how often the supervisor samples the run's activity.
var autoStallInterval = 30 * time.Second

func autoStallThreshold() time.Duration {
	if n, err := strconv.Atoi(os.Getenv(autoStallEnvVar)); err == nil && n > 0 {
		return time.Duration(n) * time.Minute
	}
	return autoStallDefault
}

// stallWatch decides when a run has stalled. A run is stalled once it has been
// silent for threshold. It fires once per episode and re-arms only when
// activity moves past the last one seen.
type stallWatch struct {
	threshold time.Duration
	seen      time.Time // newest activity observed; starts at the run's start
	notified  bool
}

// observe folds in the newest activity seen at now and reports whether a
// stall notice is due. A zero or older activity time is no news.
func (w *stallWatch) observe(activity, now time.Time) bool {
	if activity.After(w.seen) {
		w.seen, w.notified = activity, false
		return false
	}
	if w.notified || now.Sub(w.seen) < w.threshold {
		return false
	}
	w.notified = true
	return true
}

// startStallWatchdog samples activity every interval until the returned stop
// is called. onStall runs at most once per stall episode, with the silence so
// far. stop returns only after the sampler has exited, so no notice can
// follow it. The supervisor calls stop before it finalizes the run.
func startStallWatchdog(threshold, interval time.Duration, activity func() time.Time, onStall func(idle time.Duration)) (stop func()) {
	quit := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		w := stallWatch{threshold: threshold, seen: time.Now()}
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
				if w.observe(activity(), now) {
					onStall(now.Sub(w.seen))
				}
			}
		}
	}()
	return func() {
		close(quit)
		<-finished
	}
}

// autoRunLastActivity is the newest sign of life of a run: its start, its log
// file, or its session transcript, whichever moved last. The transcript is
// what moves for a headless claude or prx run, whose stdout stays silent until
// the final message. A file that cannot be stat'ed contributes nothing.
func autoRunLastActivity(t *flowdb.Task) time.Time {
	var last time.Time
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
	if t.SessionID.Valid && t.SessionID.String != "" {
		if h, err := harnessForSpawn(t); err == nil {
			if loc, ok := h.(harness.TranscriptLocator); ok {
				consider(fileMTime(loc.TranscriptPath(t.WorkDir, t.SessionID.String)))
			}
		}
	}
	return last
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
	last := autoRunLastActivity(t)
	if last.IsZero() {
		// No start time and no file to read: nothing says the run is silent.
		return 0, false
	}
	idle := now.Sub(last)
	return idle, idle >= autoStallThreshold()
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

// notifyParentOfStalledChild tells the parent that a run has gone silent. A
// parentless run is not messaged: the human hears only at done or blocked,
// and a stall is visible in `flow list` and `flow show`.
func notifyParentOfStalledChild(db *sql.DB, child *flowdb.Task, idle time.Duration) {
	if !child.ParentSlug.Valid || child.ParentSlug.String == "" {
		return
	}
	last := "no log output"
	if l := lastLogLine(child.AutoRunLog.String); l != "" {
		last = "last log: " + l
	}
	body := fmt.Sprintf("auto child %s stalled: no progress for %d min; %s; check `flow show task %s`",
		child.Slug, int(idle.Minutes()), last, child.Slug)
	closedBody := fmt.Sprintf("FYI: auto child %s of closed task %s stalled: no progress for %d min; check `flow show task %s`",
		child.Slug, child.ParentSlug.String, int(idle.Minutes()), child.Slug)
	notifyParentOfChild(db, child, body, closedBody)
}
