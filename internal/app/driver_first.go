package app

import (
	"database/sql"
	"flow/internal/flowdb"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// firstPromptNudgeText is the one-line UserPromptSubmit context an
// unbound session gets on its first human prompt (driver-first UX). The
// SessionStart hint carries the full instruction; this re-anchors it at
// the moment the user actually states work, where process skills such
// as brainstorming would otherwise win.
const firstPromptNudgeText = "flow: this session is unbound. If this prompt starts work, " +
	"create a task first (flow skill §4.2 \"Start a driver\": goal + which project; route to a live " +
	"driver only if its goal really matches), then do the work inside it. A one-off question needs no task."

// firstPromptNudge returns firstPromptNudgeText exactly once per session
// id, and only for an unbound, human-driven session: never in a bound
// session, never in a headless auto run, never for a flow/harness
// injected prompt. The once-ness is a marker file under
// <flowRoot>/sessions/nudged/ claimed with O_EXCL (no schema change);
// any error stays silent, a hook never fails loud.
func firstPromptNudge(bound *flowdb.Task) string {
	if bound != nil || isAutoRunSession(nil) {
		return ""
	}
	if hookPromptKnown && !isHumanPrompt(hookPromptKnown, hookPrompt, os.Getenv) {
		return ""
	}
	sid := currentSessionID()
	if sid == "" || strings.ContainsAny(sid, `/\`) || strings.Contains(sid, "..") {
		return ""
	}
	root, err := flowRoot()
	if err != nil {
		return ""
	}
	dir := filepath.Join(root, "sessions", "nudged")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	f, err := os.OpenFile(filepath.Join(dir, sid), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "" // already nudged (or unwritable): stay silent
	}
	f.Close()
	return firstPromptNudgeText
}

// driverLine is one live driver in `flow show project`'s drivers: block.
type driverLine struct {
	t        *flowdb.Task
	openKids int
}

// printProjectDrivers writes the "drivers:" block of `flow show project`:
// one line per driver task (tagged driver) in the project, not archived,
// not done unless all. A driver whose parent is also listed is indented
// under it; children not tagged driver are counted ("open" = not done,
// not archived), not listed.
func printProjectDrivers(db *sql.DB, projectSlug string, all bool) {
	tasks, err := flowdb.ListTasks(db, flowdb.TaskFilter{Project: projectSlug, Tag: driverTag, ExcludeDone: !all})
	if err != nil {
		fmt.Printf("drivers:     (error: %v)\n", err)
		return
	}
	if len(tasks) == 0 {
		fmt.Println("drivers:     (none)")
		return
	}
	listed := map[string]*flowdb.Task{}
	for _, t := range tasks {
		listed[t.Slug] = t
	}
	kids := map[string][]*flowdb.Task{} // parent slug -> listed sub-drivers
	var roots []*flowdb.Task
	for _, t := range tasks {
		if t.ParentSlug.Valid && listed[t.ParentSlug.String] != nil && t.ParentSlug.String != t.Slug {
			kids[t.ParentSlug.String] = append(kids[t.ParentSlug.String], t)
		} else {
			roots = append(roots, t)
		}
	}
	bySlug := func(s []*flowdb.Task) {
		sort.Slice(s, func(i, j int) bool { return s[i].Slug < s[j].Slug })
	}
	bySlug(roots)
	fmt.Println("drivers:")
	seen := map[string]bool{}
	var walk func(t *flowdb.Task, depth int)
	walk = func(t *flowdb.Task, depth int) {
		if seen[t.Slug] {
			return
		}
		seen[t.Slug] = true
		fmt.Printf("%s- %s\n", strings.Repeat("    ", depth)+"  ", formatDriverLine(db, t))
		k := kids[t.Slug]
		bySlug(k)
		for _, c := range k {
			walk(c, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	// A parent cycle among listed drivers leaves no root: print the rest flat.
	for _, t := range tasks {
		walk(t, 0)
	}
}

func formatDriverLine(db *sql.DB, t *flowdb.Task) string {
	parts := []string{fmt.Sprintf("%s (%s)", t.Name, t.Slug), t.Status}
	if t.WaitingOn.Valid && t.WaitingOn.String != "" {
		parts = append(parts, "waiting: "+t.WaitingOn.String)
	}
	n := 0
	for _, c := range openChildren(db, t.Slug) {
		if !isDriverTask(db, c.Slug) {
			n++
		}
	}
	noun := "children"
	if n == 1 {
		noun = "child"
	}
	parts = append(parts, fmt.Sprintf("%d open %s", n, noun))
	return strings.Join(parts, " · ")
}

// openChildren returns the tasks whose parent is slug that are neither
// done nor archived, sorted by slug. Errors yield nil (callers are
// informational).
func openChildren(db *sql.DB, slug string) []*flowdb.Task {
	slugs, err := flowdb.ChildTasks(db, slug)
	if err != nil {
		return nil
	}
	var out []*flowdb.Task
	for _, s := range slugs {
		c, err := flowdb.GetTask(db, s)
		if err != nil || c.Status == "done" || c.ArchivedAt.Valid {
			continue
		}
		out = append(out, c)
	}
	return out
}

// warnOpenChildren prints, for a driver task being closed, the children
// still open. Informational only: done does not cascade and still exits 0.
func warnOpenChildren(db *sql.DB, slug string) {
	if !isDriverTask(db, slug) {
		return
	}
	kids := openChildren(db, slug)
	if len(kids) == 0 {
		return
	}
	var names []string
	for _, c := range kids {
		names = append(names, fmt.Sprintf("%s (%s)", c.Slug, c.Status))
	}
	noun := "children"
	if len(kids) == 1 {
		noun = "child"
	}
	fmt.Fprintf(os.Stderr, "warning: driver %s has %d open %s: %s. Done does not close them; finish, reassign or archive them.\n",
		slug, len(kids), noun, strings.Join(names, ", "))
}

// warnDriverWithoutProject prints a stderr hint when slug is a driver task
// with no project: a driver always belongs to a project (SKILL.md §4.2).
// Informational only; the add/update still succeeds.
func warnDriverWithoutProject(db *sql.DB, slug string) {
	if !isDriverTask(db, slug) {
		return
	}
	t, err := flowdb.GetTask(db, slug)
	if err != nil || (t.ProjectSlug.Valid && t.ProjectSlug.String != "") {
		return
	}
	fmt.Fprintf(os.Stderr, "hint: %s is a driver with no project; drivers belong to a project; pass --project <slug> (flow update task %s --project <slug>)\n", slug, slug)
}
