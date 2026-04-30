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

	// Decide bootstrap vs resume based on the row we re-read inside the tx.
	// Fresh bootstrap means: either the task has no session_id, or --fresh
	// was passed. In both cases we allocate a new UUID here and claim it
	// in the DB via the status-flip UPDATE below — so the jsonl file claude
	// writes is identified deterministically by us, not scraped afterwards.
	var curSessionID sql.NullString
	if err := tx.QueryRow(`SELECT session_id FROM tasks WHERE slug=?`, task.Slug).Scan(&curSessionID); err != nil {
		fmt.Fprintf(os.Stderr, "error: re-read session_id: %v\n", err)
		return 1
	}
	needsBootstrap := !curSessionID.Valid || *fresh
	var sessionID string
	if needsBootstrap {
		id, err := newUUID()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: allocate session id: %v\n", err)
			return 1
		}
		sessionID = id
	} else {
		sessionID = curSessionID.String
	}

	now := flowdb.NowISO()
	if needsBootstrap {
		if _, err := tx.Exec(
			`UPDATE tasks SET status='in-progress',
			 status_changed_at = CASE WHEN status != 'in-progress' THEN ? ELSE status_changed_at END,
			 session_id=?, session_started=?, updated_at=?
			 WHERE slug=? AND status IN ('backlog','in-progress')`,
			now, sessionID, now, now, task.Slug,
		); err != nil {
			fmt.Fprintf(os.Stderr, "error: flip status: %v\n", err)
			return 1
		}
	} else {
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
	}
	// Re-select to capture the canonical view.
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

	if *fresh && curSessionID.Valid {
		fmt.Printf("--fresh: discarding old session %s\n", curSessionID.String)
	}

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

	// Spawn the iTerm tab.
	//
	// We shell out to `claude` directly (no wrapper). The skill on disk at
	// ~/.claude/skills/flow/SKILL.md is whatever was last installed via
	// `flow skill install` / `flow skill update`. To refresh it after
	// upgrading flow, the user runs `flow skill update` manually.
	var command string
	if needsBootstrap {
		// Fresh bootstrap path: we pre-allocated the session UUID above
		// and committed it to the DB. Passing --session-id to claude
		// makes it write its jsonl at the deterministic path
		// ~/.claude/projects/<encoded-cwd>/<sessionID>.jsonl, so there is
		// nothing to discover afterwards.
		prompt := buildBootstrapPrompt(task.Slug)
		command = fmt.Sprintf("claude --session-id %s %s", sessionID, iterm.ShellQuote(prompt))
	} else {
		// Resume path: the UUID we already have in the DB is what claude
		// used to write its existing jsonl.
		command = "claude --resume " + sessionID
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

	// Post-spawn bookkeeping, outside the main tx.
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
		fmt.Printf("Spawned %s (session %s)\n", task.Slug, sessionID)
	} else {
		fmt.Printf("Resumed %s (session %s)\n", task.Slug, sessionID)
	}
	return 0
}

// buildBootstrapPrompt composes the short first-message sent to a newly
// created Claude session. Intentionally shell-safe — no single/double
// quotes, backticks, or dollar signs — because it gets shell-quoted
// as a single positional argument to `claude`.
//
// The session's UUID is pre-allocated by `flow do` and passed via
// `claude --session-id <uuid>`, so there is no self-registration step
// here. The session loads context in order: task brief + task updates,
// then (if any) project brief + project updates, then CLAUDE.md files
// in the work_dir. The flow skill enforces this sequence too; the
// bootstrap prompt is a backup in case the skill isn't auto-activated.
func buildBootstrapPrompt(slug string) string {
	return fmt.Sprintf(
		"You are the execution session for flow task %s. Do ALL of the following in order before touching code:\n"+
			"1. Invoke the flow skill via the Skill tool. This loads the operating manual that governs how this session works: workflows, bootstrap contract, KB discipline, and scope-creep detection.\n"+
			"2. Run: flow show task. Read the file at the brief: path, AND every file listed under updates:.\n"+
			"3. If a project is listed on the task, run: flow show project <that-project-slug>. Read its brief file AND every file listed under its updates: section. The project brief gives cross-task context the task brief omits.\n"+
			"4. Read CLAUDE.md in your work_dir and any nested CLAUDE.md files under subdirectories you will modify. These override any assumption from the brief.\n"+
			"5. Only then begin work. If any brief section is blank or unclear, ASK — do not infer.",
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
