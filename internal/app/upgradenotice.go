package app

import (
	"fmt"
	"path/filepath"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness/claude"
	"flow/internal/harness/codex"
)

// Upgrade notice: when the flow binary changes under a running session,
// that session's loaded SKILL.md and SessionStart guidance are stale —
// Claude and Codex run the newly installed binary on every hook call, but
// nothing tells the model. So:
//
//	SessionStart        recordSessionVersion(): the session (re)loads the
//	                    current skill, so its seen version is set silently.
//	UserPromptSubmit,   upgradeNoticeContext(): compare-and-set the seen
//	PreToolUse          version; the one hook call whose CAS wins appends
//	                    the notice. Concurrent hooks lose and stay silent;
//	                    an unchanged version is a single SELECT.
//
// Stop does not take part: a Stop notice would need a blocked stop (an
// extra model turn) to reach the agent, and the next prompt or tool call
// delivers it anyway. A session with no row started on a flow that
// predates this record and is told once ("an earlier version").
//
// A "dev" build carries no version to compare and does nothing. The
// skill (with references/upgrade-notes.md) is already refreshed when the
// hook runs: Run calls maybeAutoUpgradeSkill before every hook command.

// upgradeNoticeMarker tags the notice so it is never taken for the human.
const upgradeNoticeMarker = "[flow-upgrade]"

// versionTracked reports whether this binary has a version to record.
func versionTracked() bool { return Version != "" && Version != "dev" }

// recordSessionVersion is SessionStart's half: set, never notify.
func recordSessionVersion() {
	sid := currentSessionID()
	if sid == "" || !versionTracked() {
		return
	}
	db, err := openBusDB()
	if err != nil {
		return
	}
	defer db.Close()
	_ = flowdb.RecordSessionVersion(db, sid, Version, time.Now())
}

// upgradeNoticeContext returns the once-per-change notice for the
// current hook's session, or "" (no change, lost the race, unknown
// session, dev build, any error). bound may be nil; it only picks the
// harness whose notes path the notice names.
func upgradeNoticeContext(bound *flowdb.Task) string {
	sid := currentSessionID()
	if sid == "" || !versionTracked() {
		return ""
	}
	db, err := openBusDB()
	if err != nil {
		return ""
	}
	defer db.Close()
	old, known, won, err := flowdb.ClaimSessionVersion(db, sid, Version, time.Now())
	if err != nil || !won {
		return ""
	}
	which := "the entries newer than " + old
	if !known {
		old, which = "an earlier version", "start with the newest entry"
	}
	if bound == nil {
		// Looked up only on a win: the per-tool-call path stays one SELECT.
		bound = lookupBoundTask()
	}
	return fmt.Sprintf(" %s flow was upgraded (%s → %s) while this session was running, so the "+
		"flow skill and session guidance you loaded are stale: re-load the flow skill now (%s) and "+
		"read %s for what changed for you (%s). This notice will not repeat.",
		upgradeNoticeMarker, old, Version, flowSkillHow(), upgradeNotesPath(bound), which)
}

// upgradeNotesPath names the installed upgrade notes for this session's
// harness. Hooks can't always tell an unbound Codex session apart, so
// outside a known Codex session the Codex copy is named too when present.
func upgradeNotesPath(bound *flowdb.Task) string {
	notes := func(skillMD string) string {
		return filepath.Join(filepath.Dir(skillMD), "references", "upgrade-notes.md")
	}
	cx := codex.New()
	if isCodexSession(bound) {
		if p, err := cx.SkillInstallPath(); err == nil {
			return notes(p)
		}
		return "references/upgrade-notes.md in the flow skill"
	}
	s := "references/upgrade-notes.md in the flow skill"
	if p, err := claude.New().SkillInstallPath(); err == nil {
		s = notes(p)
	}
	if skillInstalled(cx) {
		if p, err := cx.SkillInstallPath(); err == nil {
			s += " (Codex: " + notes(p) + ")"
		}
	}
	return s
}
