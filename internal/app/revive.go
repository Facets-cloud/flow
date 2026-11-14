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
// gate passes; otherwise r comes back unchanged, or with the reason a
// revive was refused (the human gets one FYI for a refusal: the sender is
// a headless child nobody watches).
func maybeRevive(db *sql.DB, m *flowdb.BusMessage, r wakeResult) wakeResult {
	if !wakeActive() || !isChildReport(db, m) || !isDriverTask(db, m.ToTaskSlug) {
		return r
	}
	if !r.notLive {
		noteBacklogDriver(db, m.ToTaskSlug)
		return r
	}
	if !reviveActive() {
		r.Reason += "; not revived: revive off (" + reviveOffEnv + ")"
		return r
	}
	t, err := flowdb.GetTask(db, m.ToTaskSlug)
	if err != nil || t == nil || !t.SessionID.Valid || t.SessionID.String == "" {
		return r
	}
	w := wakeAdapterFor(t)
	live, onDisk := reviveProbe(t, w)
	// Belt and braces for a session the process probes cannot see (a
	// host-owned Codex thread): hooks reporting a running turn mean live.
	// A harness that cross-checks the turn itself (Claude) already
	// answered in the probe; its hook state can be stale (a tab closed
	// mid-turn never records idle), so it is not consulted.
	if _, canCheck := w.(harness.TurnStateChecker); !canCheck {
		if st, _ := flowdb.GetSessionState(db, t.Slug); !st.Idle(time.Now(), wakeStaleBusy) {
			live = true
		}
	}
	switch {
	case live:
		// No human FYI: a live session's hooks deliver the report, and
		// "reopen it" would only hit flow do's live-session guard.
		r.Reason += "; not revived: the session may still be running"
		return r
	case !onDisk:
		return refuseRevive(db, t, r, "no transcript to resume")
	}
	return launchRevive(db, t, r)
}

// refuseRevive reports why t was not revived, to the sender and (once
// per reason while unread) to the human.
func refuseRevive(db *sql.DB, t *flowdb.Task, r wakeResult, why string) wakeResult {
	r.Reason += "; not revived: " + why
	// The dedup key carries the reason: an unread FYI for an older reason
	// must not hide a newer one.
	key := fmt.Sprintf(reviveRefusedPrefix+"%s for its child's report: %s.", t.Slug, why)
	fyiHumanOnce(db, t.Slug, key, key+" The report is still unread — reopen it with flow do "+t.Slug)
	return r
}

// noteBacklogDriver tells the human (once while unread) that a child's
// report reached a driver parked in backlog: no revive, since a human
// parked it, but nobody else would ever know the report is waiting.
func noteBacklogDriver(db *sql.DB, slug string) {
	if !reviveActive() {
		return
	}
	t, err := flowdb.GetTask(db, slug)
	if err != nil || t == nil || t.Status != "backlog" || t.ArchivedAt.Valid {
		return
	}
	key := fmt.Sprintf(reviveBacklogPrefix+"%s is in backlog", slug)
	fyiHumanOnce(db, slug, key, key+"; a child report is waiting in its inbox. flow does not revive a backlog "+
		"driver — move it to in-progress and reopen it with flow do "+slug+" when you want it handled")
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
		return refuseRevive(db, t, r, fmt.Sprintf("rate limit (%d per %s)", reviveLimit.Max, reviveLimit.Window))
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
	_ = recordReviveLaunched(db, t.Slug, os.Getpid(), pid, logPath)
	r.Woke, r.Reason = true, "revived its exited session headlessly, pid "+fmt.Sprint(pid)
	return r
}

// recordReviveLaunched records the launched supervisor's pid and log. The
// pid moves only while the row is still this claim's run (the claimant's
// pid, or the supervisor's own once it adopted the row): a supervisor that
// already finished must not be put back to 'running' with a dead pid.
func recordReviveLaunched(db *sql.DB, slug string, holderPID, pid int, logPath string) error {
	_, err := db.Exec(`UPDATE tasks SET auto_run_log=?, updated_at=?,
            auto_run_pid = CASE WHEN auto_run_status='running' AND auto_run_pid IN (?,?) THEN ? ELSE auto_run_pid END
        WHERE slug=?`, logPath, flowdb.NowISO(), holderPID, pid, pid, slug)
	return err
}

// adoptReviveRun runs first in a revive's supervisor: the claim stamped
// the SENDER's pid, so the supervisor takes the row over, or a run that
// ends before the sender records the pid could not finalize it.
func adoptReviveRun(db *sql.DB, slug string) {
	_, _ = db.Exec(`UPDATE tasks SET auto_run_pid=? WHERE slug=? AND auto_run_status='running'`, supervisorPID(), slug)
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
// popped leaves the report just as stuck. Reports whether such a report
// was found (and so already handled: revived, or refused with its FYI).
func reviveIfMailLanded(db *sql.DB, slug, startedAt string) bool {
	start, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return false
	}
	ms, err := flowdb.PendingForTask(db, slug)
	if err != nil {
		return false
	}
	for i := len(ms) - 1; i >= 0; i-- {
		m := ms[i]
		c, err := time.Parse(time.RFC3339, m.CreatedAt)
		// Second precision: a report stamped in the start second is the one
		// that triggered this revive, not mail that landed mid-run.
		if err != nil || !c.After(start) || !isChildReport(db, m) {
			continue
		}
		res := maybeRevive(db, m, wakeResult{Slug: slug, notLive: true})
		fmt.Printf("flow: mail landed during the revive: %s\n", res)
		return true
	}
	return false
}

// finishRevive runs after a revive's supervisor finalized the row. A
// failed resume is an FYI to the human (the child's mail is still unread);
// otherwise mail that landed mid-run gets its one re-check, and child
// reports the run left unread (not re-revived) are one FYI to the human.
func finishRevive(db *sql.DB, t *flowdb.Task, status, startedAt string) {
	switch status {
	case "dead":
		fyiHumanOnce(db, t.Slug, reviveFailedPrefix, fmt.Sprintf(reviveFailedPrefix+"%s for its children's mail and the headless resume "+
			"failed; the mail is still unread — reopen it with flow do %s (log under tasks/%s/auto-runs/)", t.Slug, t.Slug, t.Slug))
	case reviveStatus:
		if !reviveIfMailLanded(db, t.Slug, startedAt) {
			noteUnreadAfterRevive(db, t.Slug)
		}
	}
}

// noteUnreadAfterRevive FYIs the human (once while unread) when a revive
// ended with child reports still unread that no re-revive picked up: the
// session ignored its prompt or exited early, and the reports would
// otherwise sit silently. It never revives again.
func noteUnreadAfterRevive(db *sql.DB, slug string) {
	ms, err := flowdb.PendingForTask(db, slug)
	if err != nil {
		return
	}
	n := 0
	for _, m := range ms {
		if isChildReport(db, m) {
			n++
		}
	}
	if n == 0 {
		return
	}
	key := fmt.Sprintf(reviveUnreadPrefix+"%s ended", slug)
	fyiHumanOnce(db, slug, key, fmt.Sprintf("%s with %d child report(s) still unread. flow will not revive it "+
		"again for them — reopen it with flow do %s to handle them", key, n, slug))
}

// Human FYIs about a driver's revive, one per kind (and, for refusals,
// per reason) while unread.
const (
	reviveFailedPrefix  = "FYI: flow tried to revive driver "
	reviveRefusedPrefix = "FYI: flow did not revive driver "
	reviveUnreadPrefix  = "FYI: flow's revive of driver "
	reviveBacklogPrefix = "FYI: driver "
	reviveKilledPrefix  = "FYI: flow's revive run of driver "
)

// fyiHumanOnce sends the human an FYI from slug, unless one from slug
// starting with prefix (the dedup key) is still unread: a driver that
// keeps failing (or being refused) would otherwise repeat it on every
// child report.
func fyiHumanOnce(db *sql.DB, slug, prefix, body string) {
	if ms, err := flowdb.UnreadForHuman(db, busUser); err == nil {
		for _, m := range ms {
			if m.FromTaskSlug == slug && strings.HasPrefix(m.Body, prefix) {
				return
			}
		}
	}
	_ = flowdb.InsertBusMessage(db, &flowdb.BusMessage{
		ID: newBusID(), CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: busUser, FromTaskSlug: slug, ToAssignee: busUser, FYI: true, Body: body,
	})
}

// noteKilledRevive FYIs the human (once while unread) that a revive's
// supervisor died (kill -9, reboot) before finalizing. Not a reopen ask:
// the next child report revives the driver again.
func noteKilledRevive(db *sql.DB, slug string) {
	key := fmt.Sprintf(reviveKilledPrefix+"%s died before finishing", slug)
	fyiHumanOnce(db, slug, key, key+" (its supervisor was killed). Its children's mail stays unread until the "+
		"next child report revives it again; reopen it with flow do "+slug+" if you want it handled sooner")
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
