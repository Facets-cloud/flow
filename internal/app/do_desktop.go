package app

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	"flow/internal/desktop"
	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
)

// desktopDoOpts carries the `flow do` flags the Claude Desktop path uses.
type desktopDoOpts struct {
	fresh, force, skipPerms bool
	inject                  string
}

// Desktop session lookups, as package vars so tests can stub the
// process table without a real Claude Desktop.
var (
	desktopLiveSessions = func(h harness.Harness) (map[string]int, error) { return h.LiveSessionIDs() }
	desktopHeldSessions = claude.DesktopSessionIDs
)

// cmdDoDesktop is `flow do` when the active backend is Claude Desktop.
// A task that already has a session re-opens it with claude://resume (the
// transcript, and so the stored session id, carries over). A task without
// one, or with --fresh, gets a new Desktop Code session in its work_dir
// whose pre-filled prompt binds the session to the task via
// `flow do --here` and then runs the normal bootstrap. Desktop picks the
// new session's id itself, which is why binding happens from inside it.
func cmdDoDesktop(db *sql.DB, task *flowdb.Task, h harness.Harness, o desktopDoOpts) int {
	if h.Name() != harness.NameClaude {
		fmt.Fprintf(os.Stderr, "error: task %q uses the %s harness; Claude Desktop can only open Claude Code sessions\n", task.Slug, h.Name())
		return 1
	}
	if task.Status == "done" {
		fmt.Fprintf(os.Stderr, "error: task %q is done; edit its status back to backlog or in-progress to reopen it\n", task.Slug)
		return 1
	}
	if task.WorkDir == "" {
		fmt.Fprintf(os.Stderr, "error: task %q has no work_dir\n", task.Slug)
		return 1
	}
	if o.skipPerms {
		fmt.Fprintln(os.Stderr, "warning: --dangerously-skip-permissions is ignored in Claude Desktop; set the permission mode in the Desktop session")
	}
	if task.SessionID.Valid && task.SessionID.String != "" && !o.fresh {
		return desktopResume(db, task, h, o)
	}
	return desktopBootstrap(db, task, o)
}

// desktopResume re-opens a task's existing session in Claude Desktop.
// If the session is already live only in Desktop, re-opening its link
// just switches to it. If a terminal also holds it, opening it in Desktop
// would put two writers on one transcript, so it refuses unless --force.
func desktopResume(db *sql.DB, task *flowdb.Task, h harness.Harness, o desktopDoOpts) int {
	if o.inject != "" {
		fmt.Fprintln(os.Stderr, "error: --with/--with-file can't be delivered through Claude Desktop's resume link; send it as a flow message to the task instead")
		return 2
	}
	sid := task.SessionID.String
	switch desktopHolder(h, sid) {
	case holderTerminal:
		if !o.force {
			fmt.Fprintf(os.Stderr,
				"error: task %q's session (%s) is open in a terminal; close it there before opening it in Claude Desktop, or pass --force\n",
				task.Slug, sid)
			return 1
		}
	case holderDesktop:
		if err := desktop.OpenSession(sid); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("Already open: %s — switched to it in Claude Desktop\n", task.Slug)
		return 0
	}
	if err := desktop.OpenSession(sid); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	now := flowdb.NowISO()
	if _, err := db.Exec(
		`UPDATE tasks SET status='in-progress',
		 status_changed_at = CASE WHEN status != 'in-progress' THEN ? ELSE status_changed_at END,
		 session_last_resumed=?, updated_at=?
		 WHERE slug=? AND status IN ('backlog','in-progress')`,
		now, now, now, task.Slug,
	); err != nil {
		fmt.Fprintf(os.Stderr, "error: record resume: %v\n", err)
		return 1
	}
	bumpWorkdirUsed(db, task.WorkDir)
	fmt.Printf("Resumed %s in Claude Desktop (session %s)\n", task.Slug, sid)
	return 0
}

type sessionHolder int

const (
	holderNone sessionHolder = iota
	holderDesktop
	holderTerminal
)

// desktopHolder reports who currently runs session sid. ps failures read
// as "not live", matching the terminal path's best-effort guard.
func desktopHolder(h harness.Harness, sid string) sessionHolder {
	live, err := desktopLiveSessions(h)
	if err != nil {
		return holderNone
	}
	id := strings.ToLower(sid)
	n := live[id]
	if n == 0 {
		return holderNone
	}
	held, err := desktopHeldSessions()
	if err == nil && held[id] >= n {
		return holderDesktop
	}
	return holderTerminal
}

// desktopBootstrap opens a new Claude Desktop Code session for a task
// with no session (or --fresh). It changes nothing in the DB: the
// session binds itself with `flow do --here` once the user sends the
// pre-filled prompt, which is also what flips the task to in-progress.
func desktopBootstrap(db *sql.DB, task *flowdb.Task, o desktopDoOpts) int {
	rebind := task.SessionID.Valid && task.SessionID.String != ""
	prompt := buildDesktopBootstrapPrompt(task.Slug, rebind, bootstrapPromptForTask(db, task), o.inject)
	if err := desktop.OpenNewSession(task.WorkDir, prompt); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if rebind {
		fmt.Printf("--fresh: the new session will replace %s once it binds\n", task.SessionID.String)
	}
	fmt.Printf("Opened a new Claude Desktop session for %s in %s — send the pre-filled prompt to start; it binds itself to the task\n",
		task.Slug, task.WorkDir)
	return 0
}

// buildDesktopBootstrapPrompt prefixes the regular bootstrap prompt with
// the `flow do --here` bind step (with --force when replacing an existing
// session for --fresh) and appends any --with instruction.
func buildDesktopBootstrapPrompt(slug string, rebind bool, bootstrap, inject string) string {
	bind := "flow do --here " + slug
	if rebind {
		bind = "flow do --here --force " + slug
	}
	var b strings.Builder
	fmt.Fprintf(&b, "This is a new Claude Desktop session for flow task %s. First bind it to the task by running: %s\n", slug, bind)
	b.WriteString("If that command fails, stop and show me its error instead of continuing.\n\n")
	b.WriteString(bootstrap)
	b.WriteString("\n\nBecause this session runs in Claude Desktop, also follow the flow skill's Claude Desktop reference (sidebar grouping by project).")
	if inject != "" {
		b.WriteString("\n\nAfter the steps above, also do this:\n")
		b.WriteString(inject)
	}
	return b.String()
}
