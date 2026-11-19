package app

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flow/internal/flowdb"
)

// driverTag is the reserved tag that makes a task a DRIVER: it owns its
// outcome end to end and hands self-contained work to `flow auto`
// children. Regular bound tasks keep the lighter delegation block.
const driverTag = "driver"

// isDriverTask reports whether slug carries the reserved driver tag.
func isDriverTask(db *sql.DB, slug string) bool {
	tags, err := flowdb.GetTaskTags(db, slug)
	if err != nil {
		return false
	}
	for _, t := range tags {
		if t == driverTag {
			return true
		}
	}
	return false
}

// isDriverSlug is isDriverTask for callers without an open DB (hooks).
func isDriverSlug(slug string) bool {
	path, err := flowDBPath()
	if err != nil {
		return false
	}
	db, err := flowdb.OpenDB(path)
	if err != nil {
		return false
	}
	defer db.Close()
	return isDriverTask(db, slug)
}

// driverRoleBlock is the context a DRIVER task's session gets, at
// SessionStart, from `flow do --here`, and when the bound task is tagged
// driver mid-session. Full recipe: skill references/delegation.md.
func driverRoleBlock() string {
	return " flow DRIVER task: you own this task's outcome end to end. Hand any " +
		"self-contained unit of work (implementation, research, debugging, fix + PR + " +
		"test, scans, audits) to a headless `flow auto` child, never the harness's " +
		"built-in Agent/subagent tool: write a brief (goal, where, Done when, gates, report " +
		"via flow message, never block waiting for a reply (it reaches your inbox; " +
		"pop it when a flow-bus notice surfaces it)) then `flow auto \"<name>\" --slug <s> " +
		"--brief <file>`; announce \"spawning auto child: <slug>\" and continue without asking. " +
		"Keep with the user: the conversation, design and decisions, verifying each child's " +
		"result (it names its update note) before reporting, and live/prod applies (children " +
		"plan and gate only). Habits: never idle while children run; use the time on the " +
		"user's open decisions; when the user returns or child mail arrives, lead with what " +
		"changed (done / blocked / dead children); end any turn that needs the user with a " +
		"numbered \"Needs from you:\" list answerable as \"1 yes, 2 later\". Children cannot " +
		"spawn grandchildren. Recipe: flow skill references/delegation.md." +
		childHarnessesLine()
}

// childHarnessesLine names the harnesses a child can run on, so a driver
// can follow the user's harness preference (delegation.md). Empty with
// one harness present: the block is then unchanged.
func childHarnessesLine() string {
	hs := presentHarnesses()
	if len(hs) < 2 {
		return ""
	}
	names := make([]string, len(hs))
	for i, h := range hs {
		names[i] = string(h.Name())
	}
	return " harnesses available for children: " + strings.Join(names, ", ") +
		" (flow auto … --harness <name>; follow the user's preference)"
}

// roleBlockFor is what a bound session gets: every human-driven session
// is a driver (no tag needed) and gets ONE line pointing at the skill's
// references/driver.md (the rules live there, not in Go); auto runs (so
// delegation never recurses) and playbook runs (frozen brief) get
// nothing. The INTAKE TURN line follows on the first bind/start (first)
// or while the brief still holds the Done-when placeholder.
func roleBlockFor(t *flowdb.Task, first bool) string {
	if isAutoRunSession(t) || t.Kind == "playbook_run" {
		return ""
	}
	path := "references/driver.md in the flow skill"
	if h, err := harnessByName(string(sessionHarness(t))); err == nil {
		if skillMD, err := h.SkillInstallPath(); err == nil {
			path = filepath.Join(filepath.Dir(skillMD), "references", "driver.md") // the session's own install
		}
	}
	blk := " You are the DRIVER for this task: follow references/driver.md (" + path + ")." + childHarnessesLine()
	if first {
		return blk + "\n" + intakeTurnLine
	}
	return blk + intakeTurnBlock(t.Slug)
}

// doneWhenPlaceholder is the driver brief's "Done when" body until the
// user agrees the criteria (skill references/templates.md).
const doneWhenPlaceholder = "*Agree with the user on the first turn.*"

// intakeTurnLine is the deterministic intake stop, so every harness ends
// the intake turn at the proposal instead of building.
const intakeTurnLine = "INTAKE TURN: restate the goal, propose Done when + 'Needs from you:', then END the turn. Do not build or close anything."

// intakeTurnBlock returns intakeTurnLine (on its own line) while slug's
// brief's ## Done when still holds the placeholder, else "".
func intakeTurnBlock(slug string) string {
	root, err := flowRoot()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(root, "tasks", slug, "brief.md"))
	if err != nil {
		return ""
	}
	// Only the ## Done when section counts (a quote under Decisions is history).
	_, done, _ := strings.Cut(string(b), "## Done when")
	if i := strings.Index(done, "\n## "); i >= 0 {
		done = done[:i]
	}
	if !strings.Contains(done, doneWhenPlaceholder) {
		return ""
	}
	return "\n" + intakeTurnLine
}

// notifyParentOfDeadChild posts to the driver's inbox when a child's auto
// run ends dead. A child that crashes never sends its own report, so
// without this the driver would never hear about it. When the parent is
// no longer deliverable (done, archived, gone) the notice goes to the
// human as an FYI instead — a row addressed to a closed task is never
// read (its bus footprint was already cleaned at close-out).
func notifyParentOfDeadChild(db *sql.DB, child *flowdb.Task) {
	if !child.ParentSlug.Valid || child.ParentSlug.String == "" {
		return
	}
	parent := child.ParentSlug.String
	body := fmt.Sprintf("auto child %s ended dead without closing itself; check `flow show task %s` "+
		"and its auto-run log, then re-run or take it over", child.Slug, child.Slug)
	toSlug := parent
	if p, err := flowdb.GetTask(db, parent); err != nil || p == nil || deliverableTask(p) != nil {
		toSlug = ""
		body = fmt.Sprintf("FYI: auto child %s of closed task %s ended dead without closing itself; "+
			"check `flow show task %s` and its auto-run log", child.Slug, parent, child.Slug)
	}
	m := &flowdb.BusMessage{
		ID: newBusID(), CreatedAt: flowdb.NowISO(), Kind: "message",
		FromAssignee: busUser, FromTaskSlug: child.Slug,
		ToAssignee: busUser, ToTaskSlug: toSlug,
		Body: body, FYI: toSlug == "", // an FYI to the human never pings a connector
	}
	if err := flowdb.InsertBusMessage(db, m); err != nil {
		return
	}
	_ = flowdb.SweepBus(db, time.Now())
}
