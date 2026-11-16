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

This section applies only while this task is tagged ` + "`driver`" + `; once the
tag is removed, ignore it.

You own this task's outcome end to end. It is done when "Done when" is met
and you have verified it, not when the work has been handed off.
In a headless ` + "`flow auto`" + ` run, do the work yourself; delegation applies only to the interactive driver session.

- Delegate: hand self-contained work (implementation, research, debugging,
  fix + PR + test, scans, audits) to headless ` + "`flow auto`" + ` children, not the
  harness's built-in subagents. Keep the conversation, decisions, verifying
  results and live/prod applies yourself. How to split the work is your
  call (optional shapes: flow skill references/delegation.md).
- Children cannot spawn grandchildren.
- Don't idle while children run; use the time on open decisions.
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
- End any turn that needs the user with a numbered "Needs from you:" list.
- When Done when is met and verified, offer to close the task out.
`

// roleHeadingRe matches the Role section's ATX heading with 0-3 leading
// spaces (CommonMark; 4+ spaces or a tab is indented code): `## Role`
// alone, with optional closing #s, or followed by a `:` label
// (`## Role: driver`), so a user-edited Role section is never
// duplicated. `## Role of this service` is an unrelated heading.
var roleHeadingRe = regexp.MustCompile(`^ {0,3}##[ \t]+Role[ \t]*(:.*|#*[ \t]*)$`)

// fenceOpenRe matches a CommonMark fence opener: 0-3 spaces, then 3+
// backticks or tildes, then the info string.
var fenceOpenRe = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")

// fenceCloseRe matches a CommonMark closing fence: 0-3 spaces (no tab),
// 3+ backticks or tildes, then only spaces or tabs.
var fenceCloseRe = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})[ \t]*$")

// scanBrief reports whether brief carries a real `## Role` heading,
// skipping fenced code blocks (``` or ~~~): a `## Role: reviewer` inside
// an example child brief is not this task's Role section. openFence is
// the opener of a fence still unclosed at EOF ("" if none).
func scanBrief(brief []byte) (hasRole bool, openFence string) {
	fence := ""
	for _, line := range strings.Split(string(brief), "\n") {
		if fence != "" {
			// A closer uses the opener's character, at least as many.
			if m := fenceCloseRe.FindStringSubmatch(line); m != nil &&
				m[1][0] == fence[0] && len(m[1]) >= len(fence) {
				fence = ""
			}
			continue
		}
		// A backtick opener's info string may not contain a backtick
		// (CommonMark); tilde info strings are unrestricted.
		if m := fenceOpenRe.FindStringSubmatch(line); m != nil &&
			!(m[1][0] == '`' && strings.Contains(m[2], "`")) {
			fence = m[1]
			continue
		}
		if roleHeadingRe.MatchString(line) {
			return true, ""
		}
	}
	return false, fence
}

func hasRoleHeading(brief []byte) bool {
	has, _ := scanBrief(brief)
	return has
}

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
	return hasRoleHeading(b)
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
	has, openFence := scanBrief(b)
	if has {
		return nil
	}
	// A brief ending inside an unclosed fence would swallow the appended
	// section (and healing would re-append it every time): close it first.
	closer := ""
	if openFence != "" {
		if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
			closer = "\n"
		}
		closer += openFence + "\n"
		b = append(b, closer...)
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
	return appendToFile(p, closer+sep+driverRoleSection)
}

func appendToFile(p, text string) error {
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// healDriverRoleSection re-appends a lost Role section for a human-driven
// driver session: the §4.2 intake rewrites the stub after `--tag driver`
// and can drop it. Auto children are never touched (callers pass
// auto=true for them). Errors are best-effort: the fallback block covers
// a brief that still lacks the section.
func healDriverRoleSection(db *sql.DB, slug string, auto bool) {
	if auto || !roleSectionAllowed(db, slug) {
		return
	}
	_ = ensureDriverRoleSection(slug)
}

// roleSectionAllowed reports whether flow may append a Role section to
// slug's brief: the task is a driver and not a playbook run, whose brief
// is a frozen snapshot of the playbook.
func roleSectionAllowed(db *sql.DB, slug string) bool {
	if !isDriverTask(db, slug) {
		return false
	}
	t, err := flowdb.GetTask(db, slug)
	return err == nil && t != nil && t.Kind != "playbook_run"
}

// healDriverRoleSlug is healDriverRoleSection for callers without an
// open DB (the SessionStart hook, which already excluded auto runs).
func healDriverRoleSlug(slug string) {
	path, err := flowDBPath()
	if err != nil {
		return
	}
	db, err := flowdb.OpenDB(path)
	if err != nil {
		return
	}
	defer db.Close()
	healDriverRoleSection(db, slug, false)
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
		healDriverRoleSection(db, t.Slug, false)
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
