package app

import (
	"database/sql"
	"errors"
	"flow/internal/flowdb"
	"flow/internal/iterm"
	"fmt"
	"net/url"
	"os"
)

// openConcurrentDB opens flow.db with a generous busy_timeout so that two
// concurrent `flow do` processes (or two goroutines in the tests) will
// serialize at the SQLite file level rather than failing fast with
// SQLITE_BUSY. The pragma is applied at connection-open time via the DSN
// so every conn in the pool inherits it. Schema creation still runs via
// OpenDB to keep DDL in one place.
func openConcurrentDB(path string) (*sql.DB, error) {
	// Ensure schema exists via the shared OpenDB path.
	pre, err := flowdb.OpenDB(path)
	if err != nil {
		return nil, err
	}
	pre.Close()

	q := url.Values{}
	// 30s is enough to cover realistic bootstraps; tests finish in ms.
	q.Set("_pragma", "busy_timeout(30000)")
	// BEGIN IMMEDIATE acquires a RESERVED lock up-front, so two concurrent
	// `flow do` transactions serialize at tx.Begin() (waiting on the busy
	// timeout) instead of racing to the first write and failing.
	q.Set("_txlock", "immediate")
	dsn := "file:" + path + "?" + q.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign_keys: %w", err)
	}
	return db, nil
}

// cmdDo flips a task to in-progress, bootstraps a Claude session if
// needed (race-free via atomic UPDATE ... WHERE session_id IS ?), and
// spawns an iTerm tab to resume it. See spec §6 for the full protocol.
func cmdDo(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: do requires a task ref")
		return 2
	}
	query := args[0]
	fs := flagSet("do")
	fresh := fs.Bool("fresh", false, "discard existing session and re-bootstrap")
	dangerSkip := fs.Bool("dangerously-skip-permissions", false, "pass --dangerously-skip-permissions through to claude")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	var extraClaudeArgs []string
	if *dangerSkip {
		extraClaudeArgs = append(extraClaudeArgs, "--dangerously-skip-permissions")
	}

	dbPath, err := flowDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	db, err := openConcurrentDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer db.Close()

	task, rc := findTask(db, query)
	if rc != 0 {
		return rc
	}

	// Step 2: atomic status flip inside a transaction. Captures preSessionID
	// and other fields for later steps. Per spec §6 this commit is the
	// durability boundary — even if bootstrap or iTerm spawn fails below,
	// the task is already in 'in-progress'.
	tx, err := db.Begin()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: begin tx: %v\n", err)
		return 1
	}
	// If we don't commit by the end, rollback.
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Re-read inside the tx so we see the freshest status.
	var curStatus string
	if err := tx.QueryRow(`SELECT status FROM tasks WHERE slug = ?`, task.Slug).Scan(&curStatus); err != nil {
		fmt.Fprintf(os.Stderr, "error: re-read task: %v\n", err)
		return 1
	}
	if curStatus == "done" {
		fmt.Fprintf(os.Stderr,
			"error: task %q is done; edit its status back to backlog or in-progress to reopen it\n",
			task.Slug)
		return 1
	}

	now := flowdb.NowISO()
	if _, err := tx.Exec(
		`UPDATE tasks SET status='in-progress',
		 status_changed_at = CASE WHEN status != 'in-progress' THEN ? ELSE status_changed_at END,
		 updated_at=?
		 WHERE slug=? AND status IN ('backlog','in-progress')`,
		now, now, task.Slug,
	); err != nil {
		fmt.Fprintf(os.Stderr, "error: flip status: %v\n", err)
		return 1
	}
	// Re-select to capture the canonical view (including pre-update
	// session_id, which is our optimistic-lock witness).
	row := tx.QueryRow(`SELECT `+flowdb.TaskCols+` FROM tasks WHERE slug = ?`, task.Slug)
	fresh2, err := flowdb.ScanTask(row)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: re-select task: %v\n", err)
		return 1
	}
	task = fresh2
	if err := tx.Commit(); err != nil {
		fmt.Fprintf(os.Stderr, "error: commit: %v\n", err)
		return 1
	}
	committed = true

	preSessionID := task.SessionID // sql.NullString — may be NULL or a UUID

	// Look up project (may be nil).
	var project *flowdb.Project
	if task.ProjectSlug.Valid {
		p, err := flowdb.GetProject(db, task.ProjectSlug.String)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(os.Stderr, "error: get project: %v\n", err)
			return 1
		}
		project = p
	}

	cwd := task.WorkDir
	if cwd == "" {
		fmt.Fprintf(os.Stderr, "error: task %q has no work_dir\n", task.Slug)
		return 1
	}

	// Step 7: decide whether to spawn a fresh session or resume.
	//
	// NOTE (contract-based bootstrap): flow does NOT try to guess or
	// capture a session_id upfront. We spawn `claude "<prompt>"` with
	// no --session-id and no --resume, and the prompt instructs the
	// execution session to call `flow register-session` as its first
	// action. register-session scans the encoded-cwd dir for the
	// newest *.jsonl (its own) and writes the UUID back to the DB.
	//
	// --fresh just nulls out any existing session_id so the next
	// resume path won't try to resume a stale one.
	needsBootstrap := !preSessionID.Valid || *fresh
	if *fresh && preSessionID.Valid {
		now := flowdb.NowISO()
		if _, err := db.Exec(
			`UPDATE tasks SET session_id=NULL, session_started=NULL, updated_at=? WHERE slug=?`,
			now, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: clear stale session_id: %v\n", err)
			return 1
		}
		fmt.Printf("--fresh: discarding old session %s\n", preSessionID.String)
		task.SessionID = sql.NullString{}
	}

	// Step 8: spawn the iTerm tab.
	var command string
	if needsBootstrap {
		// Fresh bootstrap path: spawn claude interactively with the
		// bootstrap prompt as the first user message. Leave session_id
		// NULL in the DB — the execution session fills it in via
		// register-session. No UUID allocation here.
		prompt := buildBootstrapPrompt(task.Slug)
		command = fmt.Sprintf("claude %s", iterm.ShellQuote(prompt))
		fmt.Printf("Spawning fresh session for %s (session_id will self-register)\n", task.Slug)
	} else {
		// Resume path: we have a known session_id from a prior run.
		command = "claude --resume " + task.SessionID.String
	}
	if *dangerSkip {
		command += " --dangerously-skip-permissions"
	}
	envVars := map[string]string{"FLOW_TASK": task.Slug}
	if project != nil {
		envVars["FLOW_PROJECT"] = project.Slug
	}
	if err := iterm.SpawnTab(buildTabTitle(project, task), cwd, command, envVars); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Step 9: single-row updates outside any explicit transaction. These
	// are only meaningful for the resume path; for a fresh spawn the
	// session_id is still NULL and will be filled by register-session.
	now2 := flowdb.NowISO()
	if !needsBootstrap {
		if _, err := db.Exec(
			`UPDATE tasks SET session_last_resumed = ? WHERE slug = ?`,
			now2, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: record resume: %v\n", err)
			return 1
		}
	}
	if _, err := db.Exec(
		`UPDATE workdirs SET last_used_at = ? WHERE path = ?`,
		now2, task.WorkDir,
	); err != nil {
		fmt.Fprintf(os.Stderr, "warning: bump workdir last_used_at: %v\n", err)
	}

	if needsBootstrap {
		fmt.Printf("Spawned %s — execution session will self-register its session_id\n", task.Slug)
	} else {
		fmt.Printf("Resumed %s (session %s)\n", task.Slug, task.SessionID.String)
	}
	return 0
}

// buildBootstrapPrompt composes the short first-message sent to a newly
// created Claude session. Intentionally shell-safe — no single/double
// quotes, backticks, or dollar signs — because it gets shell-quoted
// as a single positional argument to `claude`.
//
// The critical first instruction is `flow register-session`. That writes
// this session's UUID back to the task row so subsequent `flow do`
// calls resume correctly. Without it, flow has no way to know what
// session_id the just-spawned claude is using.
//
// After registering, the session must load context in order: task
// brief + task updates, then (if any) project brief + project updates,
// then CLAUDE.md files in the work_dir. The flow skill enforces this
// sequence too; the bootstrap prompt is a backup in case the skill
// isn't auto-activated.
func buildBootstrapPrompt(slug string) string {
	return fmt.Sprintf(
		"You are the execution session for flow task %s. Do ALL of the following in order before touching code:\n"+
			"1. Invoke the flow skill via the Skill tool. This loads the operating manual that governs how this session works: workflows, bootstrap contract, KB discipline, and scope-creep detection. Do this FIRST, and do not skip it if step 2 later fails — the skill is independent.\n"+
			"2. Run: flow register-session  (no args, uses FLOW_TASK env var). This records your session_id so future flow do calls can resume this session.\n"+
			"3. Run: flow show task. Read the file at the brief: path, AND every file listed under updates:.\n"+
			"4. If a project is listed on the task, run: flow show project <that-project-slug>. Read its brief file AND every file listed under its updates: section. The project brief gives cross-task context the task brief omits.\n"+
			"5. Read CLAUDE.md in your work_dir and any nested CLAUDE.md files under subdirectories you will modify. These override any assumption from the brief.\n"+
			"6. Only then begin work. If any brief section is blank or unclear, ASK — do not infer.",
		slug,
	)
}

// buildTabTitle returns a short iTerm tab title. Project-scoped tasks get
// "<project-slug>/<task-slug>"; floating tasks get just "<task-slug>".
// Titles longer than 30 runes are truncated with a trailing ellipsis.
func buildTabTitle(project *flowdb.Project, task *flowdb.Task) string {
	raw := task.Slug
	if project != nil {
		raw = project.Slug + "/" + task.Slug
	}
	const maxLen = 30
	runes := []rune(raw)
	if len(runes) > maxLen {
		return string(runes[:maxLen-1]) + "…"
	}
	return raw
}

// findTask resolves a user-supplied ref to exactly one non-archived task.
// Exact alias match first, then exact slug match.
func findTask(db *sql.DB, query string) (*flowdb.Task, int) {
	t, err := ResolveTask(db, query, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return nil, 1
	}
	return t, 0
}
