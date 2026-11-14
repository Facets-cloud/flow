package app

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness"
)

// Driver revive: wake an EXITED driver session when its own auto child
// writes to it.
//
// Harness wake (wake.go) only reaches a session whose process is alive. A
// driver that exited (the human closed the tab, the machine slept, the
// harness crashed) never hears its children: their reports sit unread
// until a human reopens it, so delegated work stalls. Revive closes that
// one gap and nothing wider:
//
//	afterBusInsert ─► wakeTask: "not live" (no push channel / no Waker)
//	   │
//	   ▼  maybeRevive gates, all required:
//	revive on (FLOW_NO_WAKE and FLOW_NO_REVIVE unset) · recipient tagged
//	driver · mail is a non-FYI message from a task whose parent IS the
//	recipient · no live process for the session in ANY probe the harness
//	offers, and its hooks do not report a running turn · the session's
//	transcript exists on disk
//	   │
//	   ▼  flowdb.ClaimRevive: single-flight (auto_run_status='running')
//	   │  + rate limit (reviveLimit per task)
//	   ▼
//	detached `flow __auto-exec <slug>` with FLOW_AUTO_REVIVE=<slug>: a
//	headless resume of the SAME session with the revive prompt (pop the
//	inbox, act as the driver, end the turn). It finalizes 'revived', never
//	'dead', so a driver that simply did not close itself is not reported
//	as a crash.
//
// Mail landing while the revive runs is held by wakeTask's "auto run"
// gate; the PreToolUse hook announces it to the running session, and the
// supervisor re-checks once after exit (reviveIfMailLanded).

const (
	// reviveOffEnv opts out of revive alone (harness wake stays on).
	reviveOffEnv = "FLOW_NO_REVIVE"
	// reviveEnvVar marks a revive run's supervisor and session (value: the
	// driver's slug). It lets that session dispatch auto children (it is
	// the driver, not a child) and tells the supervisor which prompt and
	// final status to use. autoChildEnv strips it.
	reviveEnvVar = "FLOW_AUTO_REVIVE"
	// reviveStatus is the auto_run_status a revive ends with when the
	// driver did not close itself (the normal case).
	reviveStatus = "revived"
)

// reviveLimit caps revives per driver. A headless resume re-reads the
// driver's whole transcript, so each one costs real tokens.
var reviveLimit = flowdb.ReviveLimit{Window: time.Hour, Max: 4}

func reviveActive() bool { return wakeActive() && os.Getenv(reviveOffEnv) == "" }

// reviveProbe reports whether t's session may still be running (live)
// and whether its transcript exists to resume (onDisk). Any doubt counts
// as live: a headless resume next to a live tab would put two writers on
// one transcript. Tests replace it.
var reviveProbe = defaultReviveProbe

// defaultReviveProbe is the real reviveProbe.
func defaultReviveProbe(t *flowdb.Task, w harness.Waker) (live, onDisk bool) {
	sid := t.SessionID.String
	if w != nil && w.WakeLive(harness.WakeRequest{SessionID: sid, StateDir: wakeStateDir()}) {
		return true, true
	}
	h, err := harnessForTask(t)
	if err != nil {
		return true, false
	}
	if ids, err := h.LiveSessionIDs(); err != nil || ids[strings.ToLower(sid)] > 0 {
		return true, true
	}
	if tc, ok := w.(harness.TurnStateChecker); ok {
		if _, known := tc.SessionBusy(sid, wakeCrossCheckTimeout); known {
			return true, true
		}
	}
	if h.ValidateSession(t.WorkDir, sid) == nil {
		return false, true
	}
	if loc, ok := h.(harness.SessionLocator); ok {
		_, found := loc.LocateSession(sid)
		return false, found
	}
	return false, false
}

// isChildReport reports whether m is mail that may revive its recipient:
// a non-FYI message from a task the recipient spawned (its child), which
// includes flow's own dead-child notice (sent from the child's slug).
func isChildReport(db *sql.DB, m *flowdb.BusMessage) bool {
	if m.Kind != "message" || m.FYI || m.ToTaskSlug == "" || m.FromTaskSlug == "" {
		return false
	}
	from, err := flowdb.GetTask(db, m.FromTaskSlug)
	return err == nil && from != nil && from.ParentSlug.Valid && from.ParentSlug.String == m.ToTaskSlug
}

// maybeRevive upgrades a "not live" wake result into a revive when every
// gate passes; otherwise r comes back unchanged.
func maybeRevive(db *sql.DB, m *flowdb.BusMessage, r wakeResult) wakeResult {
	if !r.notLive || !reviveActive() || !isChildReport(db, m) || !isDriverTask(db, m.ToTaskSlug) {
		return r
	}
	t, err := flowdb.GetTask(db, m.ToTaskSlug)
	if err != nil || t == nil || !t.SessionID.Valid || t.SessionID.String == "" {
		return r
	}
	live, onDisk := reviveProbe(t, wakeAdapterFor(t))
	// Belt and braces for a session the process probes cannot see (a
	// host-owned Codex thread): hooks reporting a running turn mean live.
	if st, _ := flowdb.GetSessionState(db, t.Slug); !st.Idle(time.Now(), wakeStaleBusy) {
		live = true
	}
	switch {
	case live:
		r.Reason += "; not revived: the session may still be running"
		return r
	case !onDisk:
		r.Reason += "; not revived: no transcript to resume"
		return r
	}
	return launchRevive(db, t, r)
}

// launchRevive claims and starts the headless resume.
func launchRevive(db *sql.DB, t *flowdb.Task, r wakeResult) wakeResult {
	prev := flowdb.AutoRunRecord{
		Status: t.AutoRunStatus.String, Started: t.AutoRunStarted.String, Finished: t.AutoRunFinished.String,
	}
	now := time.Now()
	claim, err := flowdb.ClaimRevive(db, t.Slug, now, reviveLimit, os.Getpid())
	switch {
	case err != nil:
		r.Reason += "; not revived: " + err.Error()
		return r
	case claim == flowdb.ReviveInFlight:
		r.Reason = "auto run (it pops in the foreground)"
		return r
	case claim == flowdb.ReviveRateLimited:
		r.Reason += fmt.Sprintf("; not revived: rate limit (%d per %s)", reviveLimit.Max, reviveLimit.Window)
		return r
	case claim != flowdb.ReviveClaimed:
		return r
	}
	root, err := flowRoot()
	var pid int
	var logPath string
	if err == nil {
		pid, logPath, err = launchAutoRunEnv(t, root, "", reviveEnvVar+"="+t.Slug)
	}
	if err != nil {
		_ = flowdb.ReleaseRevive(db, t.Slug, os.Getpid(), now, prev)
		r.Reason += "; revive failed: " + err.Error()
		return r
	}
	_ = recordAutoRunLaunched(db, t.Slug, pid, logPath)
	r.Woke, r.Reason = true, "revived its exited session headlessly, pid "+fmt.Sprint(pid)
	return r
}

// isReviveRun reports whether this process belongs to the revive run of
// slug (the supervisor or the resumed session's tool shells).
func isReviveRun(slug string) bool {
	return slug != "" && os.Getenv(reviveEnvVar) == slug
}

// reviveIfMailLanded runs in the revive supervisor after the session
// exited: a child report that landed after the revive started and is
// still unread gets one more revive (rate limit applies). Unread, not
// merely unannounced: a PreToolUse notice the session saw but never
// popped leaves the report just as stuck.
func reviveIfMailLanded(db *sql.DB, slug, startedAt string) {
	start, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return
	}
	ms, err := flowdb.PendingForTask(db, slug)
	if err != nil {
		return
	}
	for i := len(ms) - 1; i >= 0; i-- {
		m := ms[i]
		c, err := time.Parse(time.RFC3339, m.CreatedAt)
		if err != nil || c.Before(start) || !isChildReport(db, m) {
			continue
		}
		res := maybeRevive(db, m, wakeResult{Slug: slug, notLive: true})
		fmt.Printf("flow: mail landed during the revive: %s\n", res)
		return
	}
}

// finishRevive runs after a revive's supervisor finalized the row. A
// failed resume is an FYI to the human (the child's mail is still unread);
// otherwise mail that landed mid-run gets its one re-check.
func finishRevive(db *sql.DB, t *flowdb.Task, status, startedAt string) {
	// The headless process is gone: whatever its hooks last recorded, no
	// turn is running now (an auto run's Stop hook never records idle).
	_ = flowdb.SetSessionState(db, t.Slug, flowdb.SessionIdle, time.Now())
	switch status {
	case "dead":
		// One FYI per unseen failure: a resume that keeps failing would
		// otherwise repeat it on every revive the rate limit allows.
		if unreadReviveFailure(db, t.Slug) {
			return
		}
		m := &flowdb.BusMessage{
			ID: newBusID(), CreatedAt: flowdb.NowISO(), Kind: "message",
			FromAssignee: busUser, FromTaskSlug: t.Slug, ToAssignee: busUser, FYI: true,
			Body: fmt.Sprintf(reviveFailedPrefix+"%s for its children's mail and the headless resume "+
				"failed; the mail is still unread — reopen it with flow do %s (log under tasks/%s/auto-runs/)", t.Slug, t.Slug, t.Slug),
		}
		_ = flowdb.InsertBusMessage(db, m)
	case reviveStatus:
		reviveIfMailLanded(db, t.Slug, startedAt)
	}
}

// reviveFailedPrefix starts the human FYI for a failed revive.
const reviveFailedPrefix = "FYI: flow tried to revive driver "

// unreadReviveFailure reports whether the human still has an unread
// failed-revive FYI for slug.
func unreadReviveFailure(db *sql.DB, slug string) bool {
	ms, err := flowdb.UnreadForHuman(db, busUser)
	if err != nil {
		return false
	}
	for _, m := range ms {
		if m.FromTaskSlug == slug && strings.HasPrefix(m.Body, reviveFailedPrefix) {
			return true
		}
	}
	return false
}

// buildRevivePrompt is the first user turn of a revive: short, because
// the resumed session already holds its whole history.
func buildRevivePrompt(slug string) string {
	return fmt.Sprintf(harness.WakeMarker+" You are the DRIVER session of flow task %[1]s, resumed HEADLESSLY by flow "+
		"because one of your auto children wrote to you while this session had exited. NO HUMAN IS WATCHING this turn.\n"+
		"- Run `flow inbox pop` in the foreground (one-shot, never --wait), repeat until it says empty, and act on each "+
		"message as the driver: verify a child's result, record decisions in an update note on %[1]s, dispatch the "+
		"next child with flow auto if the plan calls for it.\n"+
		"- Do NOT use AskUserQuestion and do NOT arm an inbox listener. If a decision needs the human, send ONE "+
		"flow message user \"<ask>\" (with --options when it is a choice) and end the turn; never wait for the reply.\n"+
		"- Keep it short: handle the mail, then end your turn. Long work goes to an auto child. Run flow done %[1]s "+
		"only if the brief's Done when is fully met and verified.\n", slug)
}
