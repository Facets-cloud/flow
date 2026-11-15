package app

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// driverRoleSection is the `## Role: driver` section flow appends as the
// LAST section of a task's brief.md when the task becomes a driver (flow
// #58). The role then lives with the task, editable per task, and the
// hook only points at it. references/templates.md carries the same text
// verbatim (TestRoleTemplateMatchesTemplatesReference).
const driverRoleSection = `## Role: driver

You own this task's outcome end to end. It is done when "Done when" is met
and you have verified it, not when the work has been handed off.

- Delegate: hand self-contained work (implementation, research, debugging,
  fix + PR + test, scans, audits) to headless ` + "`flow auto`" + ` children, not the
  harness's built-in subagents. Keep the conversation, decisions, verifying
  results and live/prod applies yourself. How to split the work is your
  call (optional shapes: flow skill references/delegation.md).
- Every child brief states: the goal; where (repo, path, branch); the
  output file you will read (an update note on its task); Done when; its
  gates; what NOT to do; where questions go; report via ` + "`flow message`" + ` at
  milestones and when done or blocked; never block waiting for a reply.
- Reuse children: before spawning, check for a child that already knows
  the area and re-ask it with ` + "`flow do --auto <slug> --with \"<question>\"`" + `;
  keep useful ones as experts.
- Verify each child's output yourself before reporting anything done.
- Gates: outward actions (push, merge, deploy, messages to others) only
  with approval of the exact text or action; a newer HOLD beats an older GO.
- Work with the user (or Command Center) the way a dev lead would. If this
  task has a parent, ask through it with ` + "`flow message`" + `, never AskUserQuestion.
- When Done when is met and verified, offer to close the task out.
`

// roleHeadingRe matches any `## Role` heading line, so a user-edited or
// renamed Role section is never duplicated.
var roleHeadingRe = regexp.MustCompile(`(?m)^##\s+Role\b`)

func taskBriefPath(slug string) (string, error) {
	root, err := flowRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "tasks", slug, "brief.md"), nil
}

// briefHasRoleSection reports whether the task's brief already carries a
// `## Role` section.
func briefHasRoleSection(slug string) bool {
	p, err := taskBriefPath(slug)
	if err != nil {
		return false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	return roleHeadingRe.Match(b)
}

// ensureDriverRoleSection appends driverRoleSection as the last section of
// the task's brief.md unless a `## Role` section is already there. It
// never rewrites existing content. Removing the driver tag leaves the
// section in place: it is the user's brief, and the hook stops pointing
// at it once the task is no longer a driver.
func ensureDriverRoleSection(slug string) error {
	p, err := taskBriefPath(slug)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if roleHeadingRe.Match(b) {
		return nil
	}
	sep := ""
	switch {
	case len(b) == 0:
	case strings.HasSuffix(string(b), "\n\n"):
	case strings.HasSuffix(string(b), "\n"):
		sep = "\n"
	default:
		sep = "\n\n"
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(sep + driverRoleSection); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// driverRolePointer is what a driver session gets when its brief carries
// the Role section: the role itself is read from the brief.
func driverRolePointer() string {
	return " flow DRIVER task: follow the ## Role section in your brief.md." +
		childHarnessesLine()
}

// driverRoleBlockFor picks the pointer when the task's brief has a Role
// section, else the full fallback block (briefs written before #58, or a
// brief overwritten without keeping the section).
func driverRoleBlockFor(slug string) string {
	if briefHasRoleSection(slug) {
		return driverRolePointer()
	}
	return driverRoleBlock()
}

// driverRoleBlock is the FALLBACK context a DRIVER task's session gets
// (SessionStart, `flow do --here`, tagging the bound task mid-session)
// when its brief has no Role section. Full recipe: skill
// references/delegation.md.
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

// roleBlockFor picks the block for a bound, human-driven session: the
// driver block for driver tasks, the light delegation block otherwise.
// Auto children get neither, so delegation never recurses.
func roleBlockFor(db *sql.DB, t *flowdb.Task) string {
	if isAutoRunSession(t) {
		return ""
	}
	if isDriverTask(db, t.Slug) {
		return driverRoleBlockFor(t.Slug)
	}
	return driverDelegationBlock()
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
