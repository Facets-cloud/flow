package flowdb

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Owner mirrors the owners table. An owner is a durable, named,
// repo-scoped self-prompting controller: each tick runs a headless run
// and self-paces its NextWakeAt; Every is only the fallback heartbeat
// floor, not a fixed schedule. It is not a single Claude session — each
// tick is a fresh run. See
// docs/superpowers/specs/2026-06-08-autonomous-owner-harness-design.md.
type Owner struct {
	Slug           string
	Name           string
	WorkDir        string
	ProjectSlug    sql.NullString
	Status         string // 'active' | 'paused' | 'retired'
	Every          string // interval, e.g. "30m"
	NextWakeAt     sql.NullString
	LastTickAt     sql.NullString
	LastTickStatus sql.NullString
	// Live-tick bookkeeping: when a tick is dispatched, TickPID holds the
	// detached `flow __owner-tick` supervisor pid and TickStarted the start
	// time; both are cleared when the tick finishes. A non-NULL TickPID
	// whose process is still alive means "a tick is running right now"
	// (read paths reconcile a dead pid — see reconcileOwnerTick).
	TickPID     sql.NullInt64
	TickStarted sql.NullString
	Harness     sql.NullString
	CreatedAt   string
	UpdatedAt   string
	ArchivedAt  sql.NullString
}

// OwnerFilter holds optional filters for ListOwners.
type OwnerFilter struct {
	Status          string
	IncludeArchived bool
}

const OwnerCols = "slug, name, work_dir, project_slug, status, every, next_wake_at, last_tick_at, last_tick_status, tick_pid, tick_started, harness, created_at, updated_at, archived_at"

func ScanOwner(row interface{ Scan(dest ...any) error }) (*Owner, error) {
	var o Owner
	err := row.Scan(
		&o.Slug, &o.Name, &o.WorkDir, &o.ProjectSlug, &o.Status, &o.Every,
		&o.NextWakeAt, &o.LastTickAt, &o.LastTickStatus, &o.TickPID, &o.TickStarted, &o.Harness,
		&o.CreatedAt, &o.UpdatedAt, &o.ArchivedAt,
	)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// CreateOwner inserts a new owner. Status defaults to 'active' and
// timestamps are stamped if unset. NextWakeAt is left as-is (NULL until
// the owner is started).
func CreateOwner(db *sql.DB, o *Owner) error {
	now := NowISO()
	if o.CreatedAt == "" {
		o.CreatedAt = now
	}
	o.UpdatedAt = now
	if o.Status == "" {
		o.Status = "active"
	}
	_, err := db.Exec(`
		INSERT INTO owners (slug, name, work_dir, project_slug, status, every,
			next_wake_at, last_tick_at, last_tick_status, tick_pid, tick_started, harness, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.Slug, o.Name, o.WorkDir, o.ProjectSlug, o.Status, o.Every,
		o.NextWakeAt, o.LastTickAt, o.LastTickStatus, o.TickPID, o.TickStarted, o.Harness, o.CreatedAt, o.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create owner %s: %w", o.Slug, err)
	}
	return nil
}

func GetOwner(db *sql.DB, slug string) (*Owner, error) {
	row := db.QueryRow("SELECT "+OwnerCols+" FROM owners WHERE slug = ?", slug)
	return ScanOwner(row)
}

// UpdateOwner persists an owner's mutable fields (name, work_dir,
// project_slug, status, every, scheduling, last-tick bookkeeping,
// harness) by slug and re-stamps updated_at. The caller loads an owner,
// mutates fields, and calls this — the single update path used by
// start/pause and by tick recording. archived_at is not touched here
// (use a dedicated archive command).
func UpdateOwner(db *sql.DB, o *Owner) error {
	o.UpdatedAt = NowISO()
	res, err := db.Exec(`
		UPDATE owners SET
			name             = ?,
			work_dir         = ?,
			project_slug     = ?,
			status           = ?,
			every            = ?,
			next_wake_at     = ?,
			last_tick_at     = ?,
			last_tick_status = ?,
			tick_pid         = ?,
			tick_started     = ?,
			harness          = ?,
			updated_at       = ?
		WHERE slug = ?`,
		o.Name, o.WorkDir, o.ProjectSlug, o.Status, o.Every,
		o.NextWakeAt, o.LastTickAt, o.LastTickStatus, o.TickPID, o.TickStarted, o.Harness, o.UpdatedAt, o.Slug,
	)
	if err != nil {
		return fmt.Errorf("update owner %s: %w", o.Slug, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("update owner %s: no such owner", o.Slug)
	}
	return nil
}

// RetireOwner permanently stops an owner: sets status='retired' and
// archives it. It no longer ticks (DueOwners requires active) and is
// hidden from the default list. On-disk files (charter, journal, tick
// logs) and any owned tasks are preserved. Errors if no such owner.
func RetireOwner(db *sql.DB, slug string) error {
	now := NowISO()
	res, err := db.Exec(
		`UPDATE owners SET status='retired', archived_at=COALESCE(archived_at, ?), updated_at=? WHERE slug=?`,
		now, now, slug,
	)
	if err != nil {
		return fmt.Errorf("retire owner %s: %w", slug, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("retire owner %s: no such owner", slug)
	}
	return nil
}

// DeleteOwner removes the owner row entirely. The caller is responsible
// for removing the on-disk owners/<slug>/ directory. Errors if no such
// owner. Owned tasks (tagged owner:<slug>) are independent and untouched.
func DeleteOwner(db *sql.DB, slug string) error {
	res, err := db.Exec(`DELETE FROM owners WHERE slug=?`, slug)
	if err != nil {
		return fmt.Errorf("delete owner %s: %w", slug, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("delete owner %s: no such owner", slug)
	}
	return nil
}

// DueOwners returns active, non-archived owners whose next_wake_at is
// set and at or before nowISO — i.e. the owners the scheduler should
// tick now. Paused/retired owners and owners that have never been
// started (NULL next_wake_at) are excluded.
//
// Comparison is done on PARSED times, not raw strings: next_wake_at may
// be stored with any timezone offset (flow's NowISO uses local tz), so a
// lexicographic string compare against a differently-offset now would be
// wrong (e.g. "…23:55+05:30" sorts after "…18:30Z" but is earlier). We
// filter the small candidate set in Go after parsing. Sorted by wake
// time so the most-overdue owner ticks first.
func DueOwners(db *sql.DB, nowISO string) ([]*Owner, error) {
	now, err := time.Parse(time.RFC3339, nowISO)
	if err != nil {
		return nil, fmt.Errorf("due owners: parse now %q: %w", nowISO, err)
	}
	q := "SELECT " + OwnerCols + ` FROM owners
		WHERE status = 'active'
		  AND archived_at IS NULL
		  AND next_wake_at IS NOT NULL
		ORDER BY next_wake_at, slug`
	rows, err := db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("due owners: %w", err)
	}
	defer rows.Close()
	var candidates []*Owner
	for rows.Next() {
		o, err := ScanOwner(rows)
		if err != nil {
			return nil, fmt.Errorf("scan owner: %w", err)
		}
		candidates = append(candidates, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []*Owner
	for _, o := range candidates {
		wake, err := time.Parse(time.RFC3339, o.NextWakeAt.String)
		if err != nil {
			// Unparseable next_wake_at: skip rather than crash the whole
			// scheduler pass on one bad row.
			continue
		}
		if !wake.After(now) { // wake <= now → due
			out = append(out, o)
		}
	}
	return out, nil
}

// ListOwners returns owners matching filter, sorted by slug. Archived
// owners are excluded unless IncludeArchived is set.
func ListOwners(db *sql.DB, filter OwnerFilter) ([]*Owner, error) {
	var where []string
	var args []any
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, filter.Status)
	}
	if !filter.IncludeArchived {
		where = append(where, "archived_at IS NULL")
	}
	q := "SELECT " + OwnerCols + " FROM owners"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY slug"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list owners: %w", err)
	}
	defer rows.Close()
	var out []*Owner
	for rows.Next() {
		o, err := ScanOwner(rows)
		if err != nil {
			return nil, fmt.Errorf("scan owner: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
