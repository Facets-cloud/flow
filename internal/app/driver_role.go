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
// children. Only flow sets it: on a human `flow add task` and on an
// interactive `flow do` / `flow do --here` (never in auto runs, playbook
// runs or owner ticks).
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

// driverRules is THE driver rule set (one source, no per-task copies):
// SessionStart, `flow do --here` and the tag switch inject it into a
// driver task's interactive session. It is never written into brief.md.
const driverRules = `You own this task's outcome end to end. It is done when "Done when" is met
and you have verified it, not when the work has been handed off.
In any headless auto run (` + "`flow auto`" + ` or ` + "`flow do --auto`" + `), do the work
yourself; delegation applies only to the interactive driver session.

- Delegate: hand self-contained work (implementation, research, debugging,
  fix + PR + test, scans, audits) to headless ` + "`flow auto`" + ` children, not the
  harness's built-in subagents. Keep the conversation, decisions, verifying
  results and live/prod applies yourself. How to split the work is your
  call; small work may be done inline (optional shapes: flow skill
  references/delegation.md).
- Spawn with ` + "`flow auto \"<name>\" --slug <s> --brief <file>`" + `; announce
  "spawning auto child: <slug>" and continue without asking.
- Children cannot spawn grandchildren.
- Don't idle while children run; use the time on open decisions. When the
  user returns or child mail arrives, lead with what changed.
- Every child brief states: the goal; where (repo, path, branch); the
  output file you will read (an update note on its task); Done when; its
  gates; what NOT to do; where questions go; report via ` + "`flow message`" + ` at
  milestones and when done or blocked; never block waiting for a reply
  (it reaches your inbox; pop it when a flow-bus notice surfaces it).
- Reuse children: before spawning, check for a child that already knows
  the area and re-ask it with ` + "`flow do --auto <slug> --with \"<question>\"`" + `;
  keep useful ones as experts.
- Verify each child's output yourself before reporting anything done.
- Gates: outward actions (push, merge, deploy, messages to others) only
  with approval of the exact text or action; a newer HOLD beats an older GO.
- Work with the user (or the parent task) the way a dev lead would. If
  this task has a parent, ask through it with ` + "`flow message`" + `,
  never AskUserQuestion.
- End any turn that needs the user with a numbered "Needs from you:" list.
- When Done when is met and verified, offer to close the task out.
`

// driverRoleBlock is the context a DRIVER task's interactive session
// gets, at SessionStart, from `flow do --here`, and when the bound task
// is tagged driver mid-session.
func driverRoleBlock() string {
	return " flow DRIVER task:\n" + driverRules + childHarnessesLine()
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

// roleBlockFor picks the block for a bound, human-driven session: the
// driver block (plus the intake-turn line while Done when is still the
// placeholder) for driver tasks, nothing otherwise. Auto children get
// nothing, so delegation never recurses.
func roleBlockFor(db *sql.DB, t *flowdb.Task) string {
	if isAutoRunSession(t) || !isDriverTask(db, t.Slug) {
		return ""
	}
	return driverRoleBlock() + intakeTurnBlock(t.Slug)
}

// doneWhenPlaceholder is the driver brief's "Done when" body until the
// user agrees the criteria (references/templates.md).
const doneWhenPlaceholder = "*Agree with the user on the first turn.*"

// intakeTurnLine is the deterministic intake stop: printed while the
// driver's brief still holds doneWhenPlaceholder, so every harness ends
// the intake turn at the proposal instead of building.
const intakeTurnLine = "INTAKE TURN: restate the goal, propose Done when + 'Needs from you:', then END the turn. Do not build or close anything."

// intakeTurnBlock returns intakeTurnLine (on its own line) while slug's
// brief still carries the placeholder, else "".
func intakeTurnBlock(slug string) string {
	root, err := flowRoot()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(root, "tasks", slug, "brief.md"))
	if err != nil || !strings.Contains(string(b), doneWhenPlaceholder) {
		return ""
	}
	return "\n" + intakeTurnLine
}

// ownerTickEnvVar marks an owner tick's environment (value: owner slug).
// Owner ticks strip FLOW_AUTO_RUN, so this is how `flow add task` inside
// a tick knows to leave the task plain.
const ownerTickEnvVar = "FLOW_OWNER_TICK"

// ownerTickEnv is the environment of a detached owner tick.
func ownerTickEnv(slug string) []string {
	return append(autoChildEnv(), ownerTickEnvVar+"="+slug)
}

// plainContext reports whether tasks created or opened from this process
// stay plain: inside an auto run or an owner tick.
func plainContext() bool {
	return os.Getenv(autoRunEnvVar) != "" || os.Getenv(ownerTickEnvVar) != ""
}

// openTagDriver adds the driver tag to t when a human opens it
// interactively (`flow do` / `flow do --here`): lazy migration of tasks
// created before driver-always. Playbook runs, auto runs, owner ticks and
// owner-managed (owner:<slug>) tasks stay plain. Reports whether the tag
// was added.
func openTagDriver(db *sql.DB, t *flowdb.Task) bool {
	if t == nil || t.Kind == "playbook_run" || plainContext() || isAutoRunSession(t) || isDriverTask(db, t.Slug) {
		return false
	}
	if tags, err := flowdb.GetTaskTags(db, t.Slug); err != nil || hasOwnerTag(tags) {
		return false // owner-managed tasks stay plain (as on add)
	}
	return flowdb.AddTaskTag(db, t.Slug, driverTag) == nil
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
