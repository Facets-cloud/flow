package app

import (
	"database/sql"
	"errors"
	"flow/internal/flowdb"
	"flow/internal/spawner"
	"fmt"
	"net/url"
	"os"
	"strings"
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
	force := fs.Bool("force", false, "open even if the task's Claude session is already running elsewhere")
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

	// Live-session guard: if this task's session_id is already running
	// in another claude process (e.g., the user has a tab open for it),
	// refuse to spawn a duplicate. --force overrides. The check is
	// best-effort: ps failures fall through silently rather than block.
	if !*force && task.SessionID.Valid && task.SessionID.String != "" {
		if live, err := liveClaudeSessions(); err == nil {
			if live[strings.ToLower(task.SessionID.String)] {
				fmt.Fprintf(os.Stderr,
					"error: task %q has a live Claude session (%s) running elsewhere — switch to that tab, or pass --force to open another\n",
					task.Slug, task.SessionID.String)
				return 1
			}
		}
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
		playbookSlug := ""
		isFirstRun := false
		if task.PlaybookSlug.Valid {
			playbookSlug = task.PlaybookSlug.String
			// First run = this is the only non-archived run-task for the
			// playbook. The current run row was just inserted by
			// cmdRunPlaybook, so a count of 1 means no prior runs exist.
			var runCount int
			if err := db.QueryRow(
				`SELECT COUNT(*) FROM tasks WHERE playbook_slug = ? AND kind = 'playbook_run' AND archived_at IS NULL`,
				playbookSlug,
			).Scan(&runCount); err != nil {
				fmt.Fprintf(os.Stderr, "warning: count playbook runs: %v\n", err)
			}
			isFirstRun = runCount <= 1
		}
		prompt := buildBootstrapPromptForKindV2(task.Slug, task.Kind, playbookSlug, isFirstRun)
		command = fmt.Sprintf("claude --session-id %s %s", sessionID, spawner.ShellQuote(prompt))
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
	if err := spawner.SpawnTab(buildTabTitle(project, task), cwd, command, envVars); err != nil {
		if needsBootstrap {
			// Spawn failed before claude could write its jsonl. Undo
			// the session_id pre-allocation so the next `flow do`
			// retries bootstrap fresh; otherwise the DB has a
			// session_id with no backing jsonl and every subsequent
			// `flow do` runs `claude --resume <uuid>` which fails
			// with "No conversation found" indefinitely. The status
			// flip is preserved: the task is genuinely in-progress
			// even when spawn fails, and the user's next attempt
			// should resume that intent.
			//
			// The WHERE clause guards against a concurrent `flow do`
			// having mutated session_id between commit and now —
			// only nil it out if it's still the UUID we allocated.
			if _, undoErr := db.Exec(
				`UPDATE tasks SET session_id=NULL, session_started=NULL WHERE slug=? AND session_id=?`,
				task.Slug, sessionID,
			); undoErr != nil {
				fmt.Fprintf(os.Stderr, "warning: rollback pre-allocated session after spawn failure: %v\n", undoErr)
			}
		}
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

// buildBootstrapPromptForKind dispatches to the right prompt variant
// based on task kind. For kind='playbook_run' the playbook variant is
// used; otherwise the regular task variant. Empty kind (legacy rows
// that somehow didn't migrate) falls through to the regular variant.
//
// The bootstrap prompt is intentionally shell-safe — no single/double
// quotes, backticks, or dollar signs — because it gets shell-quoted
// as a single positional argument to `claude`.
//
// The session's UUID is pre-allocated by `flow do` and passed via
// `claude --session-id <uuid>`, so there is no self-registration step
// here. The session loads context in order: task brief + task updates,
// then (if any) project brief + project updates, then CLAUDE.md files
// in the work_dir. The flow skill enforces this sequence too; the
// bootstrap prompt is a backup in case the skill isn't auto-activated.
// Kept for callers (and tests) that don't track first-run state. New
// callers should use buildBootstrapPromptForKindV2 to opt into the
// first-run variant when relevant.
func buildBootstrapPromptForKind(slug, kind, playbookSlug string) string {
	return buildBootstrapPromptForKindV2(slug, kind, playbookSlug, false)
}

// buildBootstrapPromptForKindV2 is the kind-aware dispatcher with first-
// run awareness for playbook runs. When isFirstRun=true on a playbook
// run, a richer "capture-aggressive" prompt is emitted that nudges the
// session to harvest scripts, edge cases, and decision rules back into
// the live playbook brief / sidecar files.
func buildBootstrapPromptForKindV2(slug, kind, playbookSlug string, isFirstRun bool) string {
	if kind == "playbook_run" {
		return buildPlaybookRunBootstrapPrompt(slug, playbookSlug, isFirstRun)
	}
	return buildTaskBootstrapPrompt(slug)
}

// buildTaskBootstrapPrompt is the prompt for regular tasks.
func buildTaskBootstrapPrompt(slug string) string {
	return fmt.Sprintf(
		"You are the execution session for flow task %s. Do ALL of the following in order before touching code:\n"+
			"1. Invoke the flow skill via the Skill tool. This loads the operating manual that governs how this session works: workflows, bootstrap contract, KB discipline, and scope-creep detection.\n"+
			"2. Run: flow show task. Read the file at the brief: path AND every file listed under updates:. Files listed under other: are sidecar references — load on demand when relevant, not eagerly.\n"+
			"3. If a project is listed on the task, run: flow show project <that-project-slug>. Read its brief AND every file under updates:. Files under other: are on-demand references.\n"+
			"4. Read CLAUDE.md in your work_dir and any nested CLAUDE.md files under subdirectories you will modify. These override any assumption from the brief.\n"+
			"5. Only then begin work. If any brief section is blank or unclear, ASK — do not infer.",
		slug,
	)
}

// buildPlaybookRunBootstrapPrompt is the prompt for playbook-run tasks.
// Adds an explicit `flow show playbook <slug>` context-load step and
// frames the run's brief as an authoritative snapshot — the session
// must execute against that snapshot, not re-read the live playbook
// brief (which may drift between runs).
func buildPlaybookRunBootstrapPrompt(runSlug, playbookSlug string, isFirstRun bool) string {
	base := fmt.Sprintf(
		"You are running playbook `%s` as run `%s`. Do ALL of the following in order before executing anything:\n"+
			"1. Invoke the flow skill via the Skill tool. This loads the operating manual that governs how this session works.\n"+
			"2. Run: flow show playbook %s. This shows the playbook's definition and recent runs — context only, not your instructions. Note any files listed under other: — they're sidecar references you can Read on demand if relevant; do not eagerly load them.\n"+
			"3. Run: flow show task. Read the file at the brief: path AND every file listed under updates:. Files under other: are references for THIS run; load on demand when relevant. The brief is your authoritative instructions for this run — it was snapshotted from the playbook at the moment this run started. Execute against this, not the live playbook brief.\n"+
			"4. If a project is listed on the task, run: flow show project <that-project-slug>. Read its brief and every file under updates:. Files under other: are on-demand references.\n"+
			"5. Read CLAUDE.md in your work_dir.\n"+
			"6. Only then begin executing your brief.\n"+
			"\n"+
			"While executing: if the user adjusts the playbook's procedure during this run (e.g. 'let's always do X', 'change the approach for...', 'this step should also...'), pause and ask via AskUserQuestion whether to persist the change to the playbook's live brief.md so future runs benefit. Options: 'Persist to playbook' (Edit playbooks/%s/brief.md), 'Just this run' (no change to live playbook), 'Both — persist + log a note in playbooks/%s/updates/'. The run's own brief.md is a frozen snapshot — never edit it to change future behavior; that's what the live playbook brief is for. See flow skill §4.13 for the full pattern.",
		playbookSlug, runSlug, playbookSlug, playbookSlug, playbookSlug,
	)

	if !isFirstRun {
		return base
	}

	firstRunAddendum := fmt.Sprintf(
		"\n"+
			"\n"+
			"⚡ THIS IS THE FIRST RUN OF THIS PLAYBOOK ⚡\n"+
			"\n"+
			"The brief was written aspirationally; this run is where the actual procedure crystallizes. Be MORE proactive than usual about capturing back to the live playbook. Specifically:\n"+
			"\n"+
			"- When you write a script, command, or settle on a concrete decision rule that wasn't in the brief: don't wait for the user to ask. Pause and AskUserQuestion whether to capture it. Three capture targets:\n"+
			"    • 'Add to playbook brief' — append/edit the relevant section of playbooks/%s/brief.md so future runs see it inline\n"+
			"    • 'Save as sidecar file' — write to playbooks/%s/<topic>.md (e.g. decision-tree.md, sample-script.md, edge-cases.md). These get surfaced under `other:` in flow show playbook for future runs to load on demand\n"+
			"    • 'Just this run' — apply locally, don't change the playbook (rare; usually means it's run-specific)\n"+
			"- When you discover an edge case or signal worth watching: AskUserQuestion whether to add it to the 'Signals to watch for' section of the live brief.\n"+
			"- Before flow done at the end of the run, AskUserQuestion: 'Capture anything from this run back to the playbook before closing?' Options: 'Yes — walk me through what to capture' / 'No, close out as-is'. The 'walk me through' path: list candidate captures (scripts produced, decisions made, edge cases hit, commands you ended up using) and offer per-item via AskUserQuestion.\n"+
			"\n"+
			"After this run, the playbook should be substantially more concrete than the aspirational brief it started with. That's the point. Treat capture-back as a primary deliverable of the first run, not an afterthought.",
		playbookSlug, playbookSlug,
	)

	return base + firstRunAddendum
}

// buildBootstrapPrompt is a backwards-compat shim for old callers that
// pass only a slug. Now points at the regular-task variant. Tests still
// call this to verify the regular variant.
func buildBootstrapPrompt(slug string) string {
	return buildTaskBootstrapPrompt(slug)
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
