package app

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness"
)

// Autonomous runs (`flow do --auto` / `flow run playbook --auto`).
//
// Instead of opening a terminal tab for a human to drive, --auto launches
// Claude headlessly in the background and walks away. The flow of control:
//
//	flow do --auto <slug>
//	  ├─ pre-allocates session_id + flips status to in-progress (same tx
//	  │  as the interactive path)
//	  ├─ autoLauncher: starts a DETACHED `flow __auto-exec <slug>` process
//	  │  (own session via Setsid, cwd = work_dir, stdout/stderr → logfile)
//	  ├─ records auto_run_status='running' + pid + started + log
//	  └─ returns immediately
//
//	flow __auto-exec <slug>   (the detached supervisor; hidden subcommand)
//	  ├─ builds the autonomous bootstrap prompt
//	  ├─ resolves the task's harness and runs harness.AutoRunArgv
//	  │     headlessly (claude: `claude --session-id <id> -p <prompt>
//	  │     --dangerously-skip-permissions`), BLOCKING until it exits
//	  └─ finalizes auto_run_status: 'completed' if the session marked the
//	     task done, else 'dead'; clears the pid
//
// Run-status lifecycle:  running ─(self `flow done`)→ completed
//
//	└─(harness exits without done / crash)→ dead
//
// Read paths (`flow show task`, `flow list tasks`) reconcile a stale
// 'running' whose supervisor pid is no longer alive to 'dead'.

// autoRunner executes a headless autonomous run via the task's harness,
// pinned to sessionID, blocking until it exits. The harness owns the
// binary + flags + injection wrapping (AutoRunArgv); the supervisor only
// runs the argv and lets stdout/stderr flow to the run log (inherited
// from the __auto-exec process the launcher pointed at the log file).
// Overridable in tests.
var autoRunner = func(h harness.Harness, sessionID, prompt string, opts harness.LaunchOpts) error {
	argv := h.AutoRunArgv(sessionID, prompt, opts)
	if len(argv) == 0 {
		return fmt.Errorf("harness %s returned empty auto-run argv", h.Name())
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// autoLauncher starts the detached `flow __auto-exec <slug>` supervisor
// and returns its pid. The child is placed in its own session (Setsid) so
// it outlives the parent `flow do --auto`, runs with cwd=workDir (so the
// claude session's jsonl lands at the path `flow transcript` expects), and
// writes stdout/stderr to logPath. Overridable in tests.
var autoLauncher = func(slug, workDir, logPath, injection string, env []string) (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("locate flow binary: %w", err)
	}
	logF, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open log %s: %w", logPath, err)
	}
	defer logF.Close()

	exArgs := []string{"__auto-exec", slug}
	if injection != "" {
		// Forward the one-off instruction to the supervisor, which appends
		// it (behind withMarker) to the autonomous prompt. Passed as a
		// distinct arg — no shell parsing — so any characters are safe.
		exArgs = append(exArgs, "--with", injection)
	}
	cmd := exec.Command(self, exArgs...)
	cmd.Dir = workDir
	cmd.Env = env
	cmd.Stdin = nil
	cmd.Stdout = logF
	cmd.Stderr = logF
	// Detached from the parent's controlling terminal/console, so it
	// survives `flow do --auto` returning. Platform-specific (see
	// proc_unix.go / proc_windows.go).
	setDetached(cmd)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start auto supervisor: %w", err)
	}
	pid := cmd.Process.Pid
	// We never Wait — the supervisor finalizes its own DB status. Release
	// so it can be reparented to init when it exits.
	_ = cmd.Process.Release()
	return pid, nil
}

// processAlive reports whether a process with the given pid is currently
// running. Used for read-time reconciliation of stale 'running' rows.
// The default implementation is platform-specific (proc_unix.go /
// proc_windows.go); the var indirection keeps it overridable in tests.
var processAlive = processAliveImpl

// autoChildEnv builds the environment for the detached supervisor. It
// inherits the parent's environment (so PATH finds claude and FLOW_ROOT
// points at the same store) but strips CLAUDE_CODE_SESSION_ID — that
// belongs to the dispatch session, not the headless run, and leaking it
// would confuse reverse-lookups inside the autonomous session.
func autoChildEnv() []string {
	in := os.Environ()
	out := make([]string, 0, len(in))
	for _, kv := range in {
		if strings.HasPrefix(kv, "CLAUDE_CODE_SESSION_ID=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// launchAutoRun creates the per-run log directory, starts the detached
// supervisor via autoLauncher, and returns (pid, logPath). It does NOT
// touch the DB — the caller records the auto_run_* fields so the write is
// part of the same bookkeeping flow as the interactive path's post-spawn
// updates.
func launchAutoRun(task *flowdb.Task, root, injection string) (int, string, error) {
	runsDir := filepath.Join(root, "tasks", task.Slug, "auto-runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return 0, "", fmt.Errorf("mkdir %s: %w", runsDir, err)
	}
	// Timestamped filename (UTC) keeps a per-run log history. time.Now is
	// fine here — this is a filename, not a slug or a stored timestamp.
	logName := time.Now().UTC().Format("2006-01-02-150405") + ".log"
	logPath := filepath.Join(runsDir, logName)

	pid, err := autoLauncher(task.Slug, task.WorkDir, logPath, injection, autoChildEnv())
	if err != nil {
		return 0, "", err
	}
	return pid, logPath, nil
}

// recordAutoRunLaunched writes the 'running' bookkeeping after a
// successful launch.
func recordAutoRunLaunched(db *sql.DB, slug string, pid int, logPath string) error {
	now := flowdb.NowISO()
	_, err := db.Exec(
		`UPDATE tasks SET auto_run_status='running', auto_run_pid=?, auto_run_started=?,
		 auto_run_finished=NULL, auto_run_log=?, updated_at=? WHERE slug=?`,
		pid, now, logPath, now, slug,
	)
	return err
}

// finalizeAutoRun records a terminal auto-run status ('completed' or
// 'dead') and clears the supervisor pid. Best-effort: errors are returned
// for the caller to log, but the run is over regardless.
func finalizeAutoRun(db *sql.DB, slug, status string) error {
	now := flowdb.NowISO()
	_, err := db.Exec(
		`UPDATE tasks SET auto_run_status=?, auto_run_finished=?, auto_run_pid=NULL, updated_at=? WHERE slug=?`,
		status, now, now, slug,
	)
	return err
}

// reconcileAutoRun promotes a stale 'running' row to 'dead' when its
// supervisor pid is no longer alive (crash, kill -9, reboot — anything
// that prevented finalizeAutoRun from running). No-op for any other
// state. Mutates both the DB and the in-memory task. Best-effort: DB
// errors leave the in-memory copy untouched.
func reconcileAutoRun(db *sql.DB, t *flowdb.Task) {
	if !t.AutoRunStatus.Valid || t.AutoRunStatus.String != "running" {
		return
	}
	if t.AutoRunPID.Valid && processAlive(int(t.AutoRunPID.Int64)) {
		return
	}
	now := flowdb.NowISO()
	if _, err := db.Exec(
		`UPDATE tasks SET auto_run_status='dead', auto_run_finished=COALESCE(auto_run_finished, ?),
		 auto_run_pid=NULL WHERE slug=? AND auto_run_status='running'`,
		now, t.Slug,
	); err != nil {
		return
	}
	t.AutoRunStatus = sql.NullString{String: "dead", Valid: true}
	if !t.AutoRunFinished.Valid {
		t.AutoRunFinished = sql.NullString{String: now, Valid: true}
	}
	t.AutoRunPID = sql.NullInt64{}
}

// cmdAutoExec is the hidden `flow __auto-exec <slug>` supervisor. It runs
// inside the detached process the launcher started: build the autonomous
// prompt, run claude headlessly to completion, then finalize the run
// status. Not listed in usage — it's an internal re-entry point.
func cmdAutoExec(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "error: __auto-exec requires a task slug")
		return 2
	}
	slug := args[0]
	fs := flagSet("__auto-exec")
	withInstr := fs.String("with", "", "one-off instruction to append to the autonomous prompt")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
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

	task, err := flowdb.GetTask(db, slug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: load task %q: %v\n", slug, err)
		return 1
	}
	if !task.SessionID.Valid || task.SessionID.String == "" {
		fmt.Fprintf(os.Stderr, "error: task %q has no session_id; cannot run headlessly\n", slug)
		_ = finalizeAutoRun(db, slug, "dead")
		return 1
	}

	playbookSlug := ""
	if task.PlaybookSlug.Valid {
		playbookSlug = task.PlaybookSlug.String
	}
	prompt := buildAutoBootstrapPrompt(slug, task.Kind, playbookSlug)

	// Resolve the task's harness (claude today; codex/gemini once their
	// adapters implement AutoRunArgv). The --with instruction rides via
	// opts.Inject so the harness wraps it behind InjectionMarker — same
	// contract as the interactive --with path.
	h, herr := harnessForSpawn(task)
	if herr != nil {
		fmt.Fprintf(os.Stderr, "error: resolve harness for %q: %v\n", slug, herr)
		_ = finalizeAutoRun(db, slug, "dead")
		return 1
	}
	opts := harness.LaunchOpts{SkipPermissions: true, Inject: *withInstr}

	runErr := autoRunner(h, task.SessionID.String, prompt, opts)

	// Re-read status: the session may have called `flow done` on itself.
	// The self-done is the authoritative success signal.
	status := "dead"
	if runErr == nil {
		if final, gerr := flowdb.GetTask(db, slug); gerr == nil && final.Status == "done" {
			status = "completed"
		}
	}
	if err := finalizeAutoRun(db, slug, status); err != nil {
		fmt.Fprintf(os.Stderr, "warning: finalize auto run %q: %v\n", slug, err)
	}

	if runErr != nil {
		fmt.Fprintf(os.Stderr, "auto run for %q failed: %v\n", slug, runErr)
		return 1
	}
	fmt.Printf("auto run for %q finished: %s\n", slug, status)
	return 0
}

// buildAutoBootstrapPrompt is the bootstrap prompt for a headless,
// unattended run. It differs from the interactive prompt in two load-
// bearing ways: (1) there is no human, so the session must NOT use
// AskUserQuestion or wait for input — it proceeds on best judgment; and
// (2) the session is responsible for closing itself out via `flow done`
// when the brief's acceptance criteria are met.
func buildAutoBootstrapPrompt(slug, kind, playbookSlug string) string {
	showStep := "2. Run: flow show task. Read the file at the brief: path AND every file under updates:. Files under other: are on-demand references."
	if kind == "playbook_run" {
		showStep = fmt.Sprintf(
			"2. Run: flow show playbook %s for context, then flow show task. Read the run brief at the brief: path (it is a frozen snapshot — your authoritative instructions) AND every file under updates:. Do NOT edit the run brief.",
			playbookSlug,
		)
	}

	return fmt.Sprintf(
		"You are an AUTONOMOUS, headless execution session for flow task %s. NO HUMAN IS WATCHING and there is no terminal to prompt. Work end to end on your own and then close yourself out.\n\n"+
			"Bootstrap (do these in order before any work):\n"+
			"1. Invoke the flow skill via the Skill tool — it governs workflows, the bootstrap contract, KB discipline, and scope-creep detection.\n"+
			showStep+"\n"+
			"3. If a project is listed on the task, run: flow show project <slug> and read its brief + updates.\n"+
			"4. Read CLAUDE.md in your work_dir and any nested CLAUDE.md under subtrees you will modify — they override the brief.\n\n"+
			"Operating rules for autonomous mode:\n"+
			"- Do NOT use AskUserQuestion and do NOT wait for user input — there is no one to answer. Where the interactive workflow would ask, decide using best engineering judgment and proceed. Resolve any deferred/unclear brief sections yourself rather than stopping.\n"+
			"- Follow the repo's conventions (build/test commands, TDD if the repo expects it). Verify your work by running the tests before considering the task complete.\n"+
			"- Be conservative with irreversible or outward-facing actions: do NOT push branches, open PRs, deploy, or message anyone unless the brief EXPLICITLY authorizes it. Make and verify the local changes; leave publishing to the user.\n"+
			"- Your objective is to reach a state where you can run `flow done`. PERSIST toward it. Keep going through transient errors, and before declaring a blocker, EXHAUST reasonable avenues: try alternative approaches, re-read the brief and CLAUDE.md, search the codebase, retry flaky steps. Only stop as a LAST RESORT — when you have genuinely tried everything and truly cannot proceed without a human decision or external access you do not have. If you must stop, write a precise progress note to the task's updates/ directory stating exactly what is blocking and what you already tried, and exit WITHOUT marking the task done (it will surface as a failed autonomous run).\n\n"+
			"Closing out:\n"+
			"- When the brief's \"Done when\" criteria are met and your changes are verified, run: flow done %s. That flips the task to done and triggers the close-out sweep (KB + project update) — it is how this autonomous run is recorded as successful. Do this yourself; no human will.\n",
		slug, slug,
	)
}
