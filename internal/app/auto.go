package app

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

	cmd := exec.Command(self, autoExecArgs(slug, logPath, injection)...)
	cmd.Dir = workDir
	cmd.Env = env
	cmd.Stdin = nil
	cmd.Stdout = logF
	cmd.Stderr = logF
	// New session → detached from the parent's controlling terminal, so
	// it survives `flow do --auto` returning.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start auto supervisor: %w", err)
	}
	pid := cmd.Process.Pid
	// We never Wait — the supervisor finalizes its own DB status. Release
	// so it can be reparented to init when it exits.
	_ = cmd.Process.Release()
	return pid, nil
}

// autoExecArgs builds the `flow __auto-exec` argv for the supervisor.
func autoExecArgs(slug, logPath, injection string) []string {
	exArgs := []string{"__auto-exec", slug, "--last-message", autoRunLastMessagePath(logPath)}
	if injection != "" {
		// Forward the one-off instruction to the supervisor, which appends
		// it (behind withMarker) to the autonomous prompt. Passed as a
		// distinct arg — no shell parsing — so any characters are safe.
		exArgs = append(exArgs, "--with", injection)
	}
	return exArgs
}

// autoRunLastMessagePath is where a headless run's final message lands:
// next to its log, same basename, `.last.txt`.
func autoRunLastMessagePath(logPath string) string {
	return strings.TrimSuffix(logPath, filepath.Ext(logPath)) + ".last.txt"
}

// supervisorPID is this supervisor's own pid: the launcher records the pid
// of the process it started, which is the `flow __auto-exec` process
// itself (no shell in between), so the row's auto_run_pid equals it.
// Overridable in tests, where the test process plays the supervisor.
var supervisorPID = os.Getpid

// afterAutoRunStatusRead is a test seam: it runs right after the
// supervisor's post-exit status read, where a late `flow done` can land.
var afterAutoRunStatusRead = func() {}

// processAlive reports whether a process with the given pid is currently
// running. Used for read-time reconciliation of stale 'running' rows.
// Overridable in tests.
var processAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 performs error checking without delivering a signal:
	// nil → alive and ours; EPERM → alive but owned by another user.
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}

// autoChildEnv builds the environment for the detached supervisor. It
// inherits the parent's environment (so PATH finds the selected harness and
// FLOW_ROOT points at the same store) but strips every registered harness
// session id. Those ids belong to the dispatch session, not the headless run,
// and leaking one would confuse reverse-lookups inside the autonomous session.
func autoChildEnv() []string {
	in := os.Environ()
	out := make([]string, 0, len(in))
	for _, kv := range in {
		// A stale auto marker from an enclosing auto run never leaks: the
		// launcher re-adds the right one for the run it starts.
		isSessionEnv := strings.HasPrefix(kv, autoRunEnvVar+"=")
		for _, h := range allHarnesses() {
			if strings.HasPrefix(kv, h.SessionIDEnvVar()+"=") {
				isSessionEnv = true
				break
			}
		}
		if !isSessionEnv {
			out = append(out, kv)
		}
	}
	return out
}

// autoRunEnvVar marks the environment of a headless auto run (value: the
// task slug). Hooks and `flow auto` read it to tell an unattended child
// from the human-driven driver session that spawned it.
const autoRunEnvVar = "FLOW_AUTO_RUN"

// isAutoRunSession reports whether the current process is (inside) a
// headless auto run: the env marker is set, or the bound task's auto run
// is live. Either signal is enough; the env marker covers a DB that has
// not caught up, the DB status covers runs launched before the marker
// existed.
func isAutoRunSession(bound *flowdb.Task) bool {
	if os.Getenv(autoRunEnvVar) != "" {
		return true
	}
	return bound != nil && bound.AutoRunStatus.Valid && bound.AutoRunStatus.String == "running"
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

	env := append(autoChildEnv(), autoRunEnvVar+"="+task.Slug)
	pid, err := autoLauncher(task.Slug, task.WorkDir, logPath, injection, env)
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
// 'dead') and clears the supervisor pid — but only while the row still
// belongs to THIS supervisor (auto_run_pid = supervisorPID(), or no pid
// recorded yet). If `flow do --auto --force` launched a newer run in the
// meantime, the row carries the newer pid and finalize is a no-op:
// owned=false tells the caller a newer run took over. Best-effort: errors
// are returned for the caller to log, but the run is over regardless.
func finalizeAutoRun(db *sql.DB, slug, status string) (owned bool, err error) {
	now := flowdb.NowISO()
	res, err := db.Exec(
		`UPDATE tasks SET auto_run_status=?, auto_run_finished=?, auto_run_pid=NULL, updated_at=?
		 WHERE slug=? AND (auto_run_pid=? OR auto_run_pid IS NULL)`,
		status, now, now, slug, supervisorPID(),
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// finalizeAutoRunUnlessDone is finalizeAutoRun(db, slug, "dead") gated on
// the task still not being done, in the same statement. owned=false means
// either a newer run owns the row or a `flow done` landed first; the caller
// re-reads the task to tell which.
func finalizeAutoRunUnlessDone(db *sql.DB, slug string) (owned bool, err error) {
	now := flowdb.NowISO()
	res, err := db.Exec(
		`UPDATE tasks SET auto_run_status='dead', auto_run_finished=?, auto_run_pid=NULL, updated_at=?
		 WHERE slug=? AND status != 'done' AND (auto_run_pid=? OR auto_run_pid IS NULL)`,
		now, now, slug, supervisorPID(),
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// reconcileAutoRun settles a stale 'running' row whose supervisor pid is
// no longer alive (crash, kill -9, reboot — anything that prevented
// finalizeAutoRun from running). A task that is already done finished its
// work (only a deferred close-out sweep may have been cut), so it becomes
// 'completed'; anything else becomes 'dead'. No-op for any other state.
// Mutates both the DB and the in-memory task. Best-effort: DB errors leave
// the in-memory copy untouched.
func reconcileAutoRun(db *sql.DB, t *flowdb.Task) {
	if !t.AutoRunStatus.Valid || t.AutoRunStatus.String != "running" {
		return
	}
	if t.AutoRunPID.Valid && processAlive(int(t.AutoRunPID.Int64)) {
		return
	}
	now := flowdb.NowISO()
	var status, finished string
	if err := db.QueryRow(
		`UPDATE tasks SET auto_run_status = CASE WHEN status='done' THEN 'completed' ELSE 'dead' END,
		 auto_run_finished=COALESCE(auto_run_finished, ?), auto_run_pid=NULL
		 WHERE slug=? AND auto_run_status='running'
		 RETURNING auto_run_status, auto_run_finished`,
		now, t.Slug,
	).Scan(&status, &finished); err != nil {
		return
	}
	t.AutoRunStatus = sql.NullString{String: status, Valid: true}
	t.AutoRunFinished = sql.NullString{String: finished, Valid: true}
	t.AutoRunPID = sql.NullInt64{}
	// The supervisor itself died (kill -9, reboot), so it never sent a
	// dead notice. This UPDATE matched exactly once, so this is the single
	// running→dead transition: tell the human about a parentless run.
	// (Parented reconcile is unchanged: no parent notice here.)
	if status == "dead" {
		notifyHumanOfDeadOrphan(db, t)
	}
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
	lastMsg := fs.String("last-message", "", "file the harness writes the run's final message to")
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
		if owned, _ := finalizeAutoRun(db, slug, "dead"); owned {
			notifyHumanOfDeadOrphan(db, task)
		}
		return 1
	}

	playbookSlug := ""
	if task.PlaybookSlug.Valid {
		playbookSlug = task.PlaybookSlug.String
	}
	parentSlug := ""
	if task.ParentSlug.Valid {
		parentSlug = task.ParentSlug.String
	}
	prompt := buildAutoBootstrapPrompt(slug, task.Kind, playbookSlug, parentSlug)

	// Resolve the task's harness (claude today; codex/gemini once their
	// adapters implement AutoRunArgv). The --with instruction rides via
	// opts.Inject so the harness wraps it behind InjectionMarker — same
	// contract as the interactive --with path.
	h, herr := harnessForSpawn(task)
	if herr != nil {
		fmt.Fprintf(os.Stderr, "error: resolve harness for %q: %v\n", slug, herr)
		if owned, _ := finalizeAutoRun(db, slug, "dead"); owned {
			notifyHumanOfDeadOrphan(db, task)
		}
		return 1
	}
	opts := harness.LaunchOpts{SkipPermissions: true, Inject: *withInstr, LastMessagePath: *lastMsg}
	// A re-run on a session that already ran must resume it.
	opts.Resume = h.ValidateSession(task.WorkDir, task.SessionID.String) == nil
	applyWorkspaceTrust(h, task.WorkDir, &opts)

	runErr := autoRunner(h, task.SessionID.String, prompt, opts)

	// Re-read status: the session may have called `flow done` on itself.
	// The self-done is the authoritative success signal.
	final, gerr := flowdb.GetTask(db, slug)
	isDone := gerr == nil && final.Status == "done"
	afterAutoRunStatusRead()
	// Not done at the read? Finalize 'dead' atomically — only while the
	// task is STILL not done. A `flow done` can land after the read while
	// this pid is alive, and it defers its sweep to us (closeoutDeferred);
	// SQLite serializes the two writes, so either the flip came first (this
	// finalize is a no-op and we re-read and take the done path below), or
	// the finalize came first (done then sees a finalized row and sweeps
	// inline). No window where a done is neither swept nor counted.
	status := "dead"
	var owned bool
	var ferr error
	finalized := false
	if !isDone {
		owned, ferr = finalizeAutoRunUnlessDone(db, slug)
		if ferr != nil || owned {
			finalized = true
		} else if late, lerr := flowdb.GetTask(db, slug); lerr == nil && late.Status == "done" {
			final, isDone = late, true // a late done won the race
		} else {
			finalized = true // a newer run (flow do --auto --force) owns the row
		}
	}
	// A done task is a completed run even if the harness then exited
	// non-zero: the child finished its work, so it is never reported dead.
	if isDone {
		status = "completed"
	}
	// Deferred close-out sweep (harness.CloseoutDeferrer, i.e. Codex): the
	// run's own `flow done` skipped the inline sweep because a nested
	// headless run inside the live harness breaks its rollout, and any
	// other `flow done` during the run left the sweep to this supervisor
	// (one sweeper). The harness has exited now, so sweep here. It runs
	// BEFORE finalize: auto_run stays 'running' (supervisor pid alive)
	// until the sweep is over, so 'completed' means the whole close-out
	// finished. Best-effort, like the inline sweep — a failure is logged
	// and never demotes the run.
	if isDone && harness.DefersCloseoutInAutoRun(h) {
		fmt.Println("flow: running deferred close-out sweep (kbs, project updates)...")
		if err := runCloseoutSweep(h, final); err != nil {
			fmt.Printf("flow: close-out sweep failed: %v\n", err)
		} else {
			fmt.Println("flow: close-out sweep done")
		}
	}
	if !finalized {
		owned, ferr = finalizeAutoRun(db, slug, status)
	}
	if ferr != nil {
		fmt.Fprintf(os.Stderr, "warning: finalize auto run %q: %v\n", slug, ferr)
	} else if !owned {
		// A newer run (flow do --auto --force) owns the row now; it
		// reports its own outcome, so this one neither stamps a status
		// nor sends a dead-child notice.
		fmt.Printf("flow: a newer auto run took over %q; leaving its status alone (this run: %s)\n", slug, status)
	}
	if status == "dead" && owned {
		notifyParentOfDeadChild(db, task)
		notifyHumanOfDeadOrphan(db, task)
	}

	if runErr != nil {
		fmt.Fprintf(os.Stderr, "auto run for %q failed: %v\n", slug, runErr)
		return 1
	}
	fmt.Printf("auto run for %q finished: %s\n", slug, status)
	return 0
}

// autoResultContract is the parented child's result contract: findings go
// in an update note on the child's own task, the final message to the
// parent says where, and the child does all of its work itself (no
// grandchildren, so delegation never recurses).
func autoResultContract(parentSlug string) string {
	return fmt.Sprintf(
		"- Result contract: write your findings or result as an update note on YOUR OWN task (tasks/<your-slug>/updates/YYYY-MM-DD-<kebab>.md, the normal updates/ mechanism) before flow done. Your final flow message user/%s must name where to find it (your slug + the update file name), body <=200 chars.\n"+
			"- Do the work yourself: do NOT spawn auto children of your own (no flow auto, no flow do --auto). Delegation is one level deep; if the work needs splitting, say so in your result and let the parent decide.\n",
		parentSlug,
	)
}

// buildAutoBootstrapPrompt is the bootstrap prompt for a headless,
// unattended run. It differs from the interactive prompt in two load-
// bearing ways: (1) there is no human, so the session must NOT use
// AskUserQuestion or wait for input — it proceeds on best judgment; and
// (2) the session is responsible for closing itself out via `flow done`
// when the brief's acceptance criteria are met. A non-empty parentSlug
// (the task was spawned from a bound session) adds a standing
// authorization to report milestones to that parent's inbox.
func buildAutoBootstrapPrompt(slug, kind, playbookSlug, parentSlug string) string {
	showStep := "2. Run: flow show task. Read the file at the brief: path AND every file under updates:. Files under other: are on-demand references."
	if kind == "playbook_run" {
		showStep = fmt.Sprintf(
			"2. Run: flow show playbook %s for context, then flow show task. Read the run brief at the brief: path (it is a frozen snapshot — your authoritative instructions) AND every file under updates:. Do NOT edit the run brief.",
			playbookSlug,
		)
	}
	// One reporting rule (authorized even if the brief is silent):
	// parent live → its inbox; parent closed → update note + an FYI to
	// the human; no parent → the human only at done/blocked.
	parentRule := "- No parent task: write your result as an update note on YOUR OWN task (tasks/<your-slug>/updates/YYYY-MM-DD-<kebab>.md). Message the human ONLY when you are done or blocked: flow message user \"<slug>: done|blocked — see <update file>\" (<=200 chars). Never message the human for routine progress.\n"
	if parentSlug != "" {
		parentRule = fmt.Sprintf(
			"- Your parent task %[1]s spawned you and is listening. Report to it (this is authorized even if the brief does not mention it): run flow message user/%[1]s \"<one line, <=200 chars>\" at each milestone, when you are done, and when you are blocked. Do not wait for a reply. If flow rejects it because the parent is closed (done/archived), still write your update note, and send your done/blocked report to the human instead as an FYI: flow message user \"FYI: <slug> done|blocked (parent %[1]s closed) — see <update file>\" --fyi. Messaging does not replace flow done.\n",
			parentSlug,
		) + autoResultContract(parentSlug)
	}

	return fmt.Sprintf(
		"You are an AUTONOMOUS, headless execution session for flow task %s. NO HUMAN IS WATCHING and there is no terminal to prompt. Work end to end on your own and then close yourself out.\n\n"+
			"Bootstrap (do these in order before any work):\n"+
			"1. "+flowSkillStep()+" — it governs workflows, the bootstrap contract, KB discipline, and scope-creep detection.\n"+
			showStep+"\n"+
			"3. If a project is listed on the task, run: flow show project <slug> and read its brief + updates.\n"+
			"4. Read "+repoInstructionFiles+" in your work_dir and any nested ones under subtrees you will modify — they override the brief.\n\n"+
			"Operating rules for autonomous mode:\n"+
			"- Do NOT use AskUserQuestion and do NOT wait for user input — there is no one to answer. Where the interactive workflow would ask, decide using best engineering judgment and proceed. Resolve any deferred/unclear brief sections yourself rather than stopping.\n"+
			"- Follow the repo's conventions (build/test commands, TDD if the repo expects it). Verify your work by running the tests before considering the task complete.\n"+
			"- Be conservative with irreversible or outward-facing actions: do NOT push branches, open PRs, deploy, or message anyone beyond the reporting rule below unless the brief EXPLICITLY authorizes it. Make and verify the local changes; leave publishing to the user.\n"+
			parentRule+
			"- Do NOT arm an inbox listener: no Monitor, no background flow inbox pop --wait loop, no flow watch (nobody reads a listener's output in a headless run, so it would ack and lose your mail; flow refuses pop --wait here). When a flow-bus notice says mail landed, and before each report and before flow done, run flow inbox pop in the foreground (one-shot, never --wait): it shows you one message; repeat until empty and act on what it says.\n"+
			"- Your objective is to reach a state where you can run `flow done`. PERSIST toward it. Keep going through transient errors, and before declaring a blocker, EXHAUST reasonable avenues: try alternative approaches, re-read the brief and "+repoInstructionFiles+", search the codebase, retry flaky steps. Only stop as a LAST RESORT — when you have genuinely tried everything and truly cannot proceed without a human decision or external access you do not have. If you must stop, write a precise progress note to the task's updates/ directory stating exactly what is blocking and what you already tried, and exit WITHOUT marking the task done (it will surface as a failed autonomous run).\n\n"+
			"Closing out:\n"+
			"- When the brief's \"Done when\" criteria are met and your changes are verified, run: flow done %s. That flips the task to done and triggers the close-out sweep (KB + project update) — it is how this autonomous run is recorded as successful. Do this yourself; no human will.\n",
		slug, slug,
	)
}
