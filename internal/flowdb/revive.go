package flowdb

import (
	"database/sql"
	"time"
)

// Revive bookkeeping (see internal/app/revive.go for the policy).
//
// A revive resumes an EXITED driver session headlessly when mail from one
// of its own auto children lands. Two guards live here:
//
//	single-flight  the claim flips tasks.auto_run_status to 'running' in
//	               one conditional UPDATE, so of two concurrent senders
//	               exactly one starts a revive; mail landing while it runs
//	               is held by the wake gate "auto run (it pops in the
//	               foreground)".
//	rate limit     bus_revives keeps one row per revive started; the claim
//	               refuses once max rows fall inside the window.
const reviveDDL = `
CREATE TABLE IF NOT EXISTS bus_revives (
    task_slug   TEXT NOT NULL,
    revived_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS bus_revives_slug ON bus_revives(task_slug, revived_at);
CREATE TABLE IF NOT EXISTS bus_child_reports (
    message_id  TEXT PRIMARY KEY,
    task_slug   TEXT NOT NULL
);`

// ReviveClaim is the outcome of ClaimRevive.
type ReviveClaim int

const (
	ReviveClaimed     ReviveClaim = iota // this caller owns the revive
	ReviveInFlight                       // an auto run (or revive) is already running
	ReviveRateLimited                    // max revives already started inside the window
	ReviveNotEligible                    // task not in-progress, archived, or gone
)

// ReviveLimit caps revives: at most Max started per Window.
type ReviveLimit struct {
	Window time.Duration
	Max    int
}

// RunClaim is the identity a claim stamps on the row before the launch:
// LogPath is the launch's reserved, never-reused log (auto_run_log, the
// run's owner: finalize, adopt and release match it), HolderPID the
// claimant, alive while it launches, so a concurrent reconcile does not
// settle the run as dead before the supervisor's pid is recorded.
type RunClaim struct {
	LogPath   string
	HolderPID int
}

// claimSetSQL is the column write every claim makes: the claim and the
// owner (auto_run_log) are one write.
const claimSetSQL = `auto_run_status='running', auto_run_log=?, auto_run_pid=?, auto_run_started=?,
            auto_run_finished=NULL, updated_at=?`

// ClaimRevive atomically claims a revive of slug: the task must be
// in-progress, unarchived, with no auto run in flight, and fewer than
// lim.Max revives started in (now-lim.Window, now]. On success the row is
// 'running', owned by c.LogPath, and a bus_revives row is recorded. The
// claim, the row and the pruning of rows outside the window are one
// transaction: a failure leaves nothing held.
func ClaimRevive(db *sql.DB, slug string, now time.Time, lim ReviveLimit, c RunClaim) (ReviveClaim, error) {
	// revived_at is UTC so the window compares as text; the task columns
	// use NowISO's layout like every other writer.
	ts := now.UTC().Format(time.RFC3339)
	local := now.Format(time.RFC3339)
	since := now.Add(-lim.Window).UTC().Format(time.RFC3339)
	tx, err := db.Begin()
	if err != nil {
		return ReviveNotEligible, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`UPDATE tasks SET `+claimSetSQL+`
        WHERE slug=? AND status='in-progress' AND archived_at IS NULL
          AND (auto_run_status IS NULL OR auto_run_status != 'running')
          AND (SELECT COUNT(*) FROM bus_revives WHERE task_slug=? AND revived_at > ?) < ?`,
		c.LogPath, c.HolderPID, local, local, slug, slug, since, lim.Max)
	if err != nil {
		return ReviveNotEligible, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		// Rows outside the window never count again: drop them so the
		// table stays bounded (at most lim.Max rows per task).
		if _, err := tx.Exec(`DELETE FROM bus_revives WHERE task_slug=? AND revived_at <= ?`, slug, since); err != nil {
			return ReviveNotEligible, err
		}
		if _, err := tx.Exec(`INSERT INTO bus_revives (task_slug, revived_at) VALUES (?,?)`, slug, ts); err != nil {
			return ReviveNotEligible, err
		}
		if err := tx.Commit(); err != nil {
			return ReviveNotEligible, err
		}
		return ReviveClaimed, nil
	}
	_ = tx.Rollback()
	return reviveLossReason(db, slug), nil
}

// reviveLossReason says why a revive claim matched no row, for the
// sender's report.
func reviveLossReason(db *sql.DB, slug string) ReviveClaim {
	var status string
	var archived, auto sql.NullString
	if err := db.QueryRow(`SELECT status, archived_at, auto_run_status FROM tasks WHERE slug=?`, slug).
		Scan(&status, &archived, &auto); err != nil {
		return ReviveNotEligible
	}
	switch {
	case status != "in-progress" || archived.Valid:
		return ReviveNotEligible
	case auto.Valid && auto.String == "running":
		return ReviveInFlight
	}
	return ReviveRateLimited
}

// AutoRunRecord is the auto-run state a claim overwrites, so a release
// can put it back ("" = NULL).
type AutoRunRecord struct {
	Status, Started, Finished, Log string
}

// ReleaseRevive undoes a claim whose launch failed: the auto-run columns
// go back to prev only while the claim still owns the row, and the
// rate-limit row is dropped.
func ReleaseRevive(db *sql.DB, slug, logPath string, claimedAt time.Time, prev AutoRunRecord) error {
	if err := ReleaseAutoRunClaim(db, slug, logPath, prev); err != nil {
		return err
	}
	// One row: another revive may share the second.
	_, err := db.Exec(`DELETE FROM bus_revives WHERE rowid =
        (SELECT rowid FROM bus_revives WHERE task_slug=? AND revived_at=? ORDER BY rowid DESC LIMIT 1)`,
		slug, claimedAt.UTC().Format(time.RFC3339))
	return err
}

// ClaimAutoRun marks slug's auto run as launched and owned by c.LogPath
// before the launch starts, so a revive (or a second --auto) sees it in
// flight and the supervisor's finalize matches the row even if it exits
// before its pid is recorded. It refuses (false) while another run is
// 'running', unless force.
func ClaimAutoRun(db *sql.DB, slug string, c RunClaim, force bool) (bool, error) {
	now := NowISO()
	res, err := db.Exec(`UPDATE tasks SET `+claimSetSQL+`
		WHERE slug=? AND (? OR auto_run_status IS NULL OR auto_run_status != 'running')`,
		c.LogPath, c.HolderPID, now, now, slug, force)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// AdoptAutoRun records pid as the run's process, only while the row is
// still 'running' and owned by logPath. The launcher calls it with the
// supervisor's pid it just started; the supervisor calls it with its own
// pid first thing. Both write the same value, so either landing is enough,
// and owned=false tells the supervisor a newer run replaced its claim.
func AdoptAutoRun(db *sql.DB, slug, logPath string, pid int) (owned bool, err error) {
	res, err := db.Exec(`UPDATE tasks SET auto_run_pid=? WHERE slug=? AND auto_run_status='running' AND auto_run_log=?`,
		pid, slug, logPath)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// AutoRunClaimState reports whether logPath's claim still holds slug's
// row (it is 'running' and owned by logPath) and the pid recorded on it
// (0 when NULL).
func AutoRunClaimState(db *sql.DB, slug, logPath string) (held bool, pid int64, err error) {
	var status, log sql.NullString
	var p sql.NullInt64
	err = db.QueryRow(`SELECT auto_run_status, auto_run_log, auto_run_pid FROM tasks WHERE slug=?`, slug).
		Scan(&status, &log, &p)
	if err != nil {
		return false, 0, err
	}
	return status.String == "running" && log.String == logPath, p.Int64, nil
}

// ReleaseAutoRunClaim puts the auto-run columns back to prev, but only
// while the claim (logPath) still owns the row and is still 'running'.
func ReleaseAutoRunClaim(db *sql.DB, slug, logPath string, prev AutoRunRecord) error {
	_, err := db.Exec(`UPDATE tasks SET auto_run_status=?, auto_run_pid=NULL, auto_run_started=?, auto_run_finished=?,
            auto_run_log=?
        WHERE slug=? AND auto_run_status='running' AND auto_run_log=?`,
		NullIfEmpty(prev.Status), NullIfEmpty(prev.Started), NullIfEmpty(prev.Finished), NullIfEmpty(prev.Log),
		slug, logPath)
	return err
}

// Permission modes recorded in tasks.permission_mode.
const (
	PermissionDefault = "default"
	PermissionBypass  = "bypass"
)

// SetPermissionMode records how slug's own session was launched.
func SetPermissionMode(db interface {
	Exec(string, ...any) (sql.Result, error)
}, slug, mode string) error {
	_, err := db.Exec(`UPDATE tasks SET permission_mode=? WHERE slug=?`, mode, slug)
	return err
}

// PermissionMode reads slug's recorded launch mode ("" when unknown).
func PermissionMode(db *sql.DB, slug string) string {
	var m sql.NullString
	if err := db.QueryRow(`SELECT permission_mode FROM tasks WHERE slug=?`, slug).Scan(&m); err != nil {
		return ""
	}
	return m.String
}

// IsReviveRun reports whether the auto run of slug that started at
// started (the task's auto_run_started, RFC3339) was a revive: every
// revive claim stamps a bus_revives row at the same second (in UTC). A
// run older than the rate-limit window may have lost its row to pruning
// and reads as not a revive.
func IsReviveRun(db *sql.DB, slug, started string) bool {
	ts, err := time.Parse(time.RFC3339, started)
	if err != nil {
		return false
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM bus_revives WHERE task_slug=? AND revived_at=?`,
		slug, ts.UTC().Format(time.RFC3339)).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// MarkChildReport records that message id, addressed to slug, qualified as
// a child report when it was SENT (its sender was a running auto child
// then). Later checks (the revive's post-run re-check, the unread FYI) read
// this mark instead of re-judging the sender, whose run has usually
// finished by then. Marks of messages that no longer exist are pruned.
func MarkChildReport(db *sql.DB, slug, id string) error {
	if _, err := db.Exec(`INSERT OR IGNORE INTO bus_child_reports (message_id, task_slug) VALUES (?,?)`, id, slug); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM bus_child_reports WHERE task_slug=? AND message_id NOT IN (SELECT id FROM bus_messages)`, slug)
	return err
}

// WasChildReport reports whether message id was marked a child report
// when it was sent.
func WasChildReport(db *sql.DB, id string) bool {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM bus_child_reports WHERE message_id=?`, id).Scan(&n)
	return err == nil && n > 0
}
