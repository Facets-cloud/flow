package app

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
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
		// The *os.PathError already names logPath.
		return 0, fmt.Errorf("open log: %w", err)
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

// autoExecArgs builds the `flow __auto-exec` argv for the supervisor. The
// log path is the launch's identity: the supervisor finalizes only the row
// that records this log, and derives the last-message path from it.
func autoExecArgs(slug, logPath, injection string) []string {
	exArgs := []string{"__auto-exec", slug, "--log", logPath}
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
// session id and the launching session's harness env (harnessSessionEnv).
// Those belong to the dispatch session, not the headless run: a leaked id
// confuses reverse-lookups inside the autonomous session, and a leaked
// socket or pid points wake at the wrong process.
func autoChildEnv() []string {
	in := os.Environ()
	out := make([]string, 0, len(in))
	for _, kv := range in {
		if !isSessionEnv(kv) {
			out = append(out, kv)
		}
	}
	return out
}

// harnessSessionEnv names the per-session variables a harness exports
// beyond its session id: the live session's messaging socket, token and
// pid, and its nesting markers. They describe the session that LAUNCHED
// the run (for a revive: the child that wrote to its driver), so a
// detached run inheriting them would record or push to the wrong
// session's endpoint.
var harnessSessionEnv = []string{
	"CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN",
	"CLAUDE_PID", "CLAUDECODE", "CLAUDE_CODE_CHILD_SESSION",
}

// isSessionEnv reports whether kv belongs to the launching session (see
// autoChildEnv).
func isSessionEnv(kv string) bool {
	// A stale auto marker from an enclosing auto run never leaks: the
	// launcher re-adds the right one for the run it starts.
	if strings.HasPrefix(kv, autoRunEnvVar+"=") || strings.HasPrefix(kv, reviveEnvVar+"=") ||
		strings.HasPrefix(kv, reviveTriggerEnv+"=") {
		return true
	}
	for _, h := range allHarnesses() {
		if strings.HasPrefix(kv, h.SessionIDEnvVar()+"=") {
			return true
		}
	}
	for _, k := range harnessSessionEnv {
		if strings.HasPrefix(kv, k+"=") {
			return true
		}
	}
	return false
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

// reserveRunLog creates task's auto-runs directory and reserves a log path
// no other launch uses. The path is the launch's identity: the claim
// stamps it on the row (auto_run_log) before the supervisor starts, and
// finalize, adopt and release all match it.
func reserveRunLog(task *flowdb.Task, root string) (string, error) {
	runsDir := filepath.Join(root, "tasks", task.Slug, "auto-runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", runsDir, err)
	}
	return reserveAutoRunLog(runsDir)
}

// startAutoRun starts the detached supervisor for the claimed launch
// logPath and returns its pid. extraEnv adds KEY=VALUE entries (a revive
// marks itself with reviveEnvVar). It does NOT touch the DB: the caller
// claimed the row first and confirms the launch after.
func startAutoRun(task *flowdb.Task, logPath, injection string, extraEnv ...string) (int, error) {
	env := append(autoChildEnv(), autoRunEnvVar+"="+task.Slug)
	env = append(env, extraEnv...)
	return autoLauncher(task.Slug, task.WorkDir, logPath, injection, env)
}

// autoRunRecorder records the launched supervisor's pid on its claimed row
// (flowdb.AdoptAutoRun). Tests replace it to make the record fail.
var autoRunRecorder = flowdb.AdoptAutoRun

// autoRunKiller stops a supervisor whose launch could not be recorded
// (its whole process group: it runs in its own session). Tests replace it.
var autoRunKiller = func(pid int) error { return syscall.Kill(-pid, syscall.SIGTERM) }

// autoAdoptWait bounds how long confirmAutoRunLaunch waits for the
// supervisor to adopt its row itself when the launcher's record failed.
var (
	autoAdoptWait = 10 * time.Second
	autoAdoptPoll = 100 * time.Millisecond
)

// confirmAutoRunLaunch records the supervisor pid on the claimed row. The
// claim stamped the LAUNCHER's pid (alive only until this command exits),
// so an unrecorded run would later be reconciled dead while it is still
// running, and its session claimed by a second run. If the record fails,
// it waits for the supervisor's own adoption (its first DB write); if that
// never lands either, the supervisor is stopped and an error returned, so
// no live run is left behind a row that does not know it. A row no longer
// held by this claim (the run already finalized, or --force replaced it)
// needs nothing.
func confirmAutoRunLaunch(db *sql.DB, slug, logPath string, pid int) error {
	err := retryTransientDB(func() error {
		_, e := autoRunRecorder(db, slug, logPath, pid)
		return e
	})
	if err == nil {
		return nil
	}
	for deadline := time.Now().Add(autoAdoptWait); time.Now().Before(deadline); time.Sleep(autoAdoptPoll) {
		if held, got, rerr := flowdb.AutoRunClaimState(db, slug, logPath); rerr == nil && (!held || got == int64(pid)) {
			return nil
		}
	}
	_ = autoRunKiller(pid)
	return fmt.Errorf("record auto run (supervisor pid %d stopped): %w", pid, err)
}

// launchClaimedRun starts the supervisor for a claimed launch and confirms
// it. On any failure the claim is released back to prev and the error
// returned; the caller reports it.
func launchClaimedRun(db *sql.DB, task *flowdb.Task, l autoLaunch) (int, error) {
	pid, err := startAutoRun(task, l.logPath, l.injection, l.extraEnv...)
	if err == nil {
		if err = confirmAutoRunLaunch(db, task.Slug, l.logPath, pid); err == nil {
			return pid, nil
		}
	}
	if rerr := l.release(); rerr != nil {
		fmt.Fprintf(os.Stderr, "warning: release auto-run claim: %v\n", rerr)
	}
	return 0, err
}

// autoLaunch is one claimed launch: its log (identity), the one-off
// instruction, extra supervisor env, and how to undo the claim.
type autoLaunch struct {
	logPath   string
	injection string
	extraEnv  []string
	release   func() error
}

// autoRunRecordOf snapshots the auto-run state a claim overwrites.
func autoRunRecordOf(t *flowdb.Task) flowdb.AutoRunRecord {
	return flowdb.AutoRunRecord{Status: t.AutoRunStatus.String, Started: t.AutoRunStarted.String,
		Finished: t.AutoRunFinished.String, Log: t.AutoRunLog.String}
}

// maxAutoRunLogAttempts bounds the -N suffix search in reserveAutoRunLog.
const maxAutoRunLogAttempts = 1000

// reserveAutoRunLog creates an empty log file named by the UTC second, with
// a -2, -3, ... suffix when that second is taken. O_EXCL makes the name a
// reservation: a second `flow do --auto --force` in the same second gets its
// own log, so its last-message file and its launch identity never collide.
func reserveAutoRunLog(runsDir string) (string, error) {
	base := time.Now().UTC().Format("2006-01-02-150405")
	for n := 1; n <= maxAutoRunLogAttempts; n++ {
		name := base + ".log"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.log", base, n)
		}
		path := filepath.Join(runsDir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create log %s: %w", path, err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("close log %s: %w", path, err)
		}
		return path, nil
	}
	return "", fmt.Errorf("no free auto-run log name under %s for %s", runsDir, base)
}

// recordAutoRunLaunched writes a launch's 'running' bookkeeping
// unconditionally: the row now belongs to logPath, whatever it held. The
// launch paths claim first and confirm with confirmAutoRunLaunch; tests use
// this to stand in for a newer launch replacing a run.
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
// 'dead') and clears the pid — but only on the row that records THIS
// launch's log (auto_run_log). The log path is the launch identity: it is
// reserved exclusively and never reused, so a retry from an earlier launch
// can never match a later one, whatever pid or status the later launch
// left. If `flow do --auto --force` launched a newer run in the meantime,
// the row records the newer log and finalize is a no-op: owned=false tells
// the caller a newer run took over. Best-effort: errors are returned for
// the caller to log, but the run is over regardless.
func finalizeAutoRun(db *sql.DB, slug, logPath, status string) (owned bool, err error) {
	now := flowdb.NowISO()
	err = retryTransientDB(func() error {
		res, e := db.Exec(
			`UPDATE tasks SET auto_run_status=?, auto_run_finished=?, auto_run_pid=NULL, updated_at=?
			 WHERE slug=? AND auto_run_log=?`,
			status, now, now, slug, logPath,
		)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		owned = n > 0
		return e
	})
	return owned, err
}

// finalizeAutoRunUnlessDone is finalizeAutoRun(db, slug, status) gated on
// the task still not being done, in the same statement. owned=false means
// either a newer run owns the row or a `flow done` landed first; the caller
// re-reads the task to tell which.
func finalizeAutoRunUnlessDone(db *sql.DB, slug, logPath, status string) (owned bool, err error) {
	now := flowdb.NowISO()
	err = retryTransientDB(func() error {
		res, e := db.Exec(
			`UPDATE tasks SET auto_run_status=?, auto_run_finished=?, auto_run_pid=NULL, updated_at=?
			 WHERE slug=? AND auto_run_log=? AND status != 'done'`,
			status, now, now, slug, logPath,
		)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		owned = n > 0
		return e
	})
	return owned, err
}

// dbRetryBudget and dbRetryBackoff bound retryTransientDB. A finalize that
// hits a transient SQLite error (SQLITE_BUSY, SQLITE_CANTOPEN, seen when a
// supervisor races a concurrent flow process) is retried with doubling
// backoff rather than leaving the row stuck in 'running'. The budget is one
// shared deadline across all attempts; an attempt already in flight may
// still run to its own busy timeout (30s, see openConcurrentDB).
var (
	dbRetryBudget  = 10 * time.Second
	dbRetryBackoff = 200 * time.Millisecond
)

// retryTransientDB runs fn, retrying only transient SQLite errors until the
// shared dbRetryBudget deadline. Any other error returns immediately.
func retryTransientDB(fn func() error) error {
	deadline := time.Now().Add(dbRetryBudget)
	backoff := dbRetryBackoff
	for {
		err := fn()
		if err == nil || !isTransientDBErr(err) || !time.Now().Before(deadline) {
			return err
		}
		wait := backoff
		if remaining := time.Until(deadline); wait > remaining {
			wait = remaining
		}
		time.Sleep(wait)
		backoff *= 2
	}
}

// SQLite primary result codes (sqlite3.h) that mean "try again": another
// connection holds the database (BUSY, LOCKED) or a concurrent process made
// the file momentarily unopenable (CANTOPEN, seen in the field).
const (
	sqliteBusy    = 5
	sqliteLocked  = 6
	sqliteCantOpn = 14
)

// isTransientDBErr classifies by SQLite result code, not message text. The
// low byte of a code is its primary code; extended codes carry it there.
func isTransientDBErr(err error) bool {
	// Any error carrying an SQLite result code (*sqlite.Error does).
	var se interface{ Code() int }
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code() & 0xff {
	case sqliteBusy, sqliteLocked, sqliteCantOpn:
		return true
	}
	return false
}

// getTaskRetry reads a task, retrying transient SQLite errors.
func getTaskRetry(db *sql.DB, slug string) (*flowdb.Task, error) {
	var t *flowdb.Task
	err := retryTransientDB(func() error {
		var e error
		t, e = flowdb.GetTask(db, slug)
		return e
	})
	return t, err
}

// autoRunBlockedLine returns the BLOCKED: line that says a run that did not
// finish is stuck rather than crashed, or "" if there is none. The signal is
// the run's authoritative final message: the file the harness wrote for
// --last-message when it exists (codex -o), otherwise the run's own log
// (claude -p prints only the final message to stdout, and the log is
// exclusive to this launch). A line counts if it starts with BLOCKED:
// (leading whitespace ignored), so a closing line after the blocker does not
// hide it. A BLOCKED: that is not at the start of a line does not count, and
// neither does a waiting_on field: a human may set that while the run is live.
func autoRunBlockedLine(lastMsg, logPath string) string {
	path := lastMsg
	if path == "" || !statOK(path) {
		path = logPath
	}
	if path == "" {
		return ""
	}
	return finalMessageBlockedLine(path)
}

// finalMessageBlockedLine returns the last BLOCKED: line in path, trimmed.
// Last, not first: a prx log holds the whole stream, so an earlier quoted
// line can precede the final declaration.
func finalMessageBlockedLine(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	blocked := ""
	for _, line := range strings.Split(string(data), "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "BLOCKED:") {
			blocked = t
		}
	}
	return blocked
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
	started := t.AutoRunStarted.String
	var status, finished string
	// Match the snapshot's owner (auto_run_log, the launch identity) and
	// pid: a stale snapshot must not settle a newer run that replaced it,
	// nor a run whose supervisor adopted the row (recorded its own pid)
	// after the claimant that this snapshot saw exited.
	if err := db.QueryRow(
		`UPDATE tasks SET auto_run_status = CASE WHEN status='done' THEN 'completed' ELSE 'dead' END,
		 auto_run_finished=COALESCE(auto_run_finished, ?), auto_run_pid=NULL
		 WHERE slug=? AND auto_run_status='running' AND auto_run_log IS ? AND auto_run_pid IS ?
		 RETURNING auto_run_status, auto_run_finished`,
		now, t.Slug, t.AutoRunLog, t.AutoRunPID,
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
	// A killed REVIVE is not a crashed task: its driver's mail is still
	// unread and the next child report (often the very send reconciling
	// it) revives it again, so the human gets an FYI, not a reopen ask.
	switch {
	case status == "dead" && flowdb.IsReviveRun(db, t.Slug, started):
		noteKilledRevive(db, t.Slug)
	case status == "dead":
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
	logPath := fs.String("log", "", "this launch's log path (its identity; the last-message file sits beside it)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *logPath == "" {
		fmt.Fprintln(os.Stderr, "error: __auto-exec requires --log <path>")
		return 2
	}
	lastMsg := autoRunLastMessagePath(*logPath)

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

	// Adopt the claimed row first (record this supervisor's pid on the row
	// owned by --log): the claim stamped the launcher's pid, alive only
	// until it exits. Not held any more means a newer run replaced the
	// claim; it owns the session, so start no harness.
	if rc, ok := adoptClaimedRun(db, slug, *logPath); !ok {
		return rc
	}

	task, err := flowdb.GetTask(db, slug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: load task %q: %v\n", slug, err)
		return 1
	}
	if !task.SessionID.Valid || task.SessionID.String == "" {
		fmt.Fprintf(os.Stderr, "error: task %q has no session_id; cannot run headlessly\n", slug)
		if owned, _ := finalizeAutoRun(db, slug, *logPath, "dead"); owned {
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
	// A revive (revive.go) resumes a driver to handle its children's mail:
	// its own short prompt, and it ends 'revived' rather than 'dead'.
	revive := isReviveRun(slug)
	reviveStarted := task.AutoRunStarted.String
	if revive {
		prompt = buildRevivePrompt(slug)
	}

	// Resolve the task's harness (claude today; codex/gemini once their
	// adapters implement AutoRunArgv). The --with instruction rides via
	// opts.Inject so the harness wraps it behind InjectionMarker — same
	// contract as the interactive --with path.
	h, herr := harnessForSpawn(task)
	if herr != nil {
		fmt.Fprintf(os.Stderr, "error: resolve harness for %q: %v\n", slug, herr)
		if owned, _ := finalizeAutoRun(db, slug, *logPath, "dead"); owned {
			notifyHumanOfDeadOrphan(db, task)
		}
		return 1
	}
	// An auto run is unattended and always skips permissions. A revive
	// resumes the driver's own session, so it keeps the mode the driver was
	// launched with (unknown → prompts, i.e. not skipped).
	skip := !revive || flowdb.PermissionMode(db, slug) == flowdb.PermissionBypass
	opts := harness.LaunchOpts{SkipPermissions: skip, Inject: *withInstr, LastMessagePath: lastMsg}
	applyTaskModel(task, &opts)
	// A re-run on a session that already ran must resume it, from the
	// directory it lives in. This process was launched in work_dir and the
	// harness inherits its cwd, so move there first when they differ.
	resumeCwd, sessionOnDisk := resumeTarget(h, task, task.SessionID.String)
	opts.Resume = sessionOnDisk
	if resumeCwd != task.WorkDir {
		if err := os.Chdir(resumeCwd); err != nil {
			fmt.Fprintf(os.Stderr, "warning: chdir %s: %v; resuming from work_dir\n", resumeCwd, err)
		}
	}
	applyWorkspaceTrust(h, task.WorkDir, &opts)

	runStart := time.Now()
	runErr := autoRunner(h, task.SessionID.String, prompt, opts)
	markIdleAfterRun(db, slug, runStart)

	// Re-read status: the session may have called `flow done` on itself.
	// The self-done is the authoritative success signal. Retried: a
	// transient read error here would otherwise skip the blocked check.
	final, gerr := getTaskRetry(db, slug)
	isDone := gerr == nil && final.Status == "done"
	afterAutoRunStatusRead()
	// Not done at the read? Finalize 'dead' atomically — only while the
	// task is STILL not done. A `flow done` can land after the read while
	// this pid is alive, and it defers its sweep to us (closeoutDeferred);
	// SQLite serializes the two writes, so either the flip came first (this
	// finalize is a no-op and we re-read and take the done path below), or
	// the finalize came first (done then sees a finalized row and sweeps
	// inline). No window where a done is neither swept nor counted.
	blockedLine := ""
	if !isDone {
		blockedLine = autoRunBlockedLine(lastMsg, *logPath)
	}
	status := "dead"
	switch {
	case revive && runErr == nil:
		status = reviveStatus
	case blockedLine != "":
		status = "blocked"
	}
	var owned bool
	var ferr error
	finalized := false
	if !isDone {
		owned, ferr = finalizeAutoRunUnlessDone(db, slug, *logPath, status)
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
		owned, ferr = finalizeAutoRun(db, slug, *logPath, status)
	}
	if ferr != nil {
		fmt.Fprintf(os.Stderr, "warning: finalize auto run %q: %v\n", slug, ferr)
		// A blocked run whose status did not land is not a blocked run we can
		// report: say so and fail, rather than claiming "finished: blocked".
		if status == "blocked" {
			fmt.Fprintf(os.Stderr, "auto run for %q is blocked but its status could not be recorded\n", slug)
			return 1
		}
	} else if !owned {
		// A newer run (flow do --auto --force) owns the row now; it
		// reports its own outcome, so this one neither stamps a status
		// nor sends a dead-child notice.
		fmt.Printf("flow: a newer auto run took over %q; leaving its status alone (this run: %s)\n", slug, status)
	}
	switch {
	case revive && owned:
		finishRevive(db, task, status, reviveStarted)
	case status == "dead" && owned:
		notifyParentOfDeadChild(db, task)
		notifyHumanOfDeadOrphan(db, task)
	}
	if status == "blocked" && owned {
		notifyOfBlockedRun(db, task, blockedLine)
	}

	if runErr != nil && status != "blocked" {
		fmt.Fprintf(os.Stderr, "auto run for %q failed: %v\n", slug, runErr)
		return 1
	}
	fmt.Printf("auto run for %q finished: %s\n", slug, status)
	return 0
}

// adoptClaimedRun records this supervisor's pid on the row its launch
// claimed (--log). ok=false means do not run: rc is 0 when a newer run
// replaced the claim (it owns the session), 1 when the row could not be
// read or written (the launcher then stops this process and releases).
func adoptClaimedRun(db *sql.DB, slug, logPath string) (rc int, ok bool) {
	var owned bool
	err := retryTransientDB(func() error {
		var e error
		owned, e = flowdb.AdoptAutoRun(db, slug, logPath, os.Getpid())
		return e
	})
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "error: adopt auto run %q: %v\n", slug, err)
		return 1, false
	case !owned:
		fmt.Fprintf(os.Stderr, "note: auto run of %q superseded by a newer run (or no longer claimed); not starting\n", slug)
		return 0, false
	}
	return 0, true
}

// markIdleAfterRun records the session idle once the headless process has
// exited (an auto run's Stop hook never does, and a stale 'busy' would hold
// off wake and revive for wakeStaleBusy) — unless the session showed
// activity after the run started. The run's own hooks record none (see
// busPreToolUseContext), so that activity is someone else's: a human who
// opened the same session with --force, whose 'busy' must stand.
func markIdleAfterRun(db *sql.DB, slug string, runStart time.Time) {
	st, err := flowdb.GetSessionState(db, slug)
	if err != nil || st.LastActivity.After(runStart) {
		return
	}
	_ = flowdb.SetSessionState(db, slug, flowdb.SessionIdle, time.Now())
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
			"- Your objective is to reach a state where you can run `flow done`. PERSIST toward it. Keep going through transient errors, and before declaring a blocker, EXHAUST reasonable avenues: try alternative approaches, re-read the brief and "+repoInstructionFiles+", search the codebase, retry flaky steps. Only stop as a LAST RESORT — when you have genuinely tried everything and truly cannot proceed without a human decision or external access you do not have. If you must stop, write a precise progress note to the task's updates/ directory stating exactly what is blocking and what you already tried. Put a line starting `BLOCKED:` naming what is blocking in your final message, and exit WITHOUT marking the task done. flow records a run that ends this way as blocked (not dead, and no dead-child alarm is sent).\n\n"+
			"Closing out:\n"+
			"- When the brief's \"Done when\" criteria are met and your changes are verified, run: flow done %s. That flips the task to done and triggers the close-out sweep (KB + project update) — it is how this autonomous run is recorded as successful. Do this yourself; no human will.\n",
		slug, slug,
	)
}
