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
CREATE INDEX IF NOT EXISTS bus_revives_slug ON bus_revives(task_slug, revived_at);`

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

// ClaimRevive atomically claims a revive of slug: the task must be
// in-progress, unarchived, with no auto run in flight, and fewer than
// lim.Max revives started in (now-lim.Window, now]. On success auto_run_status is
// 'running' with auto_run_pid=holderPID (the claimant, alive while it
// launches, so a concurrent reconcile does not mark the run dead) and a
// bus_revives row is recorded. The claim, the row and the pruning of rows
// outside the window are one transaction: a failure leaves nothing held.
func ClaimRevive(db *sql.DB, slug string, now time.Time, lim ReviveLimit, holderPID int) (ReviveClaim, error) {
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
	res, err := tx.Exec(`UPDATE tasks SET auto_run_status='running', auto_run_pid=?, auto_run_started=?,
            auto_run_finished=NULL, updated_at=?
        WHERE slug=? AND status='in-progress' AND archived_at IS NULL
          AND (auto_run_status IS NULL OR auto_run_status != 'running')
          AND (SELECT COUNT(*) FROM bus_revives WHERE task_slug=? AND revived_at > ?) < ?`,
		holderPID, local, local, slug, slug, since, lim.Max)
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
	// Lost: say why, for the sender's report.
	var status string
	var archived sql.NullString
	var auto sql.NullString
	if err := db.QueryRow(`SELECT status, archived_at, auto_run_status FROM tasks WHERE slug=?`, slug).
		Scan(&status, &archived, &auto); err != nil {
		return ReviveNotEligible, nil
	}
	switch {
	case status != "in-progress" || archived.Valid:
		return ReviveNotEligible, nil
	case auto.Valid && auto.String == "running":
		return ReviveInFlight, nil
	}
	return ReviveRateLimited, nil
}

// AutoRunRecord is the auto-run state a claim overwrites, so a release
// can put it back ("" = NULL).
type AutoRunRecord struct {
	Status, Started, Finished string
}

// ReleaseRevive undoes a claim whose launch failed: the auto-run columns
// go back to prev only while the claimant still holds the row, and the
// rate-limit row is dropped.
func ReleaseRevive(db *sql.DB, slug string, holderPID int, claimedAt time.Time, prev AutoRunRecord) error {
	if _, err := db.Exec(`UPDATE tasks SET auto_run_status=?, auto_run_pid=NULL, auto_run_started=?, auto_run_finished=?
        WHERE slug=? AND auto_run_status='running' AND auto_run_pid=?`,
		NullIfEmpty(prev.Status), NullIfEmpty(prev.Started), NullIfEmpty(prev.Finished), slug, holderPID); err != nil {
		return err
	}
	// One row: another revive may share the second.
	_, err := db.Exec(`DELETE FROM bus_revives WHERE rowid =
        (SELECT rowid FROM bus_revives WHERE task_slug=? AND revived_at=? ORDER BY rowid DESC LIMIT 1)`,
		slug, claimedAt.UTC().Format(time.RFC3339))
	return err
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
