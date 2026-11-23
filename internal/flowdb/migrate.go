package flowdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Versioned schema migrations.
//
// flow.db carries its schema version in `PRAGMA user_version`. OpenDB reads
// it once; when it equals latestVersion() the open is a single shared-lock
// read — no probes, no DDL, no write lock. Otherwise the pending migrations
// run exactly once, under one BEGIN IMMEDIATE transaction, after a
// VACUUM INTO backup of the pre-migration file.
//
// Rules for adding a migration (also in AGENTS.md):
//   - Append only. Never edit, reorder or remove a shipped migration.
//   - Additive changes (new table, new nullable/defaulted column, new
//     index) are non-breaking: older binaries keep working on the DB.
//   - Mark Breaking: true only when an older binary would corrupt or
//     misread the new shape. That raises schema_meta.min_binary_schema
//     and older binaries refuse the DB with "run `flow upgrade`".
//   - A table rebuild copies columns from pragma_table_info dynamically,
//     never from a hard-coded list (unknown/legacy columns must survive).
//   - A data migration lives in the same migration as its schema change.
//   - No schema or data-fix writes outside migrate.go / baseline.go
//     (TestNoSchemaWritesOutsideMigrations enforces it).

// Migration is one step of the schema history. Up runs inside the
// migrator's transaction with foreign_keys OFF; it must not COMMIT,
// ROLLBACK or change pragmas that can't run in a transaction.
type Migration struct {
	Version  int
	Name     string
	Breaking bool
	Up       func(q querier) error
}

// migrations is the ordered schema history. Version N is migrations[N-1].
// A var (not const) only so tests can append a failing step.
var migrations = []Migration{
	{Version: 1, Name: "baseline", Up: baseline},
	{Version: 2, Name: "model-effort", Up: migrateModelEffort},
}

// migrateModelEffort (v2) adds the harness model / effort level picked with
// --model/--effort: tasks.model/effort (a spawn or resume; later launches
// reuse them) and owners.model/effort (every tick runs on them). Nullable,
// NULL = harness default; additive, so older binaries keep working. The
// exists-checks are for DBs written by the pre-versioning build that
// shipped these columns as per-open probes (#175): they carry the columns
// at user_version 0 and pass through v1 then here.
func migrateModelEffort(q querier) error {
	for _, c := range []struct{ table, col string }{
		{"tasks", "model"}, {"tasks", "effort"},
		{"owners", "model"}, {"owners", "effort"},
	} {
		if _, err := addColumnIfMissing(q, c.table, c.col,
			`ALTER TABLE `+c.table+` ADD COLUMN `+c.col+` TEXT`); err != nil {
			return err
		}
	}
	return nil
}

func latestVersion() int { return len(migrations) }

// LatestSchemaVersion is the schema version this binary migrates to.
func LatestSchemaVersion() int { return latestVersion() }

// compatFloor is the newest Breaking migration in the given history, or 0:
// a binary whose latest version is below it must not open the DB.
func compatFloor(ms []Migration) int {
	f := 0
	for _, m := range ms {
		if m.Breaking && m.Version > f {
			f = m.Version
		}
	}
	return f
}

// ErrSchemaTooNew is returned by OpenDB when flow.db was migrated by a
// newer flow whose changes this binary cannot safely run against.
var ErrSchemaTooNew = errors.New("flow.db schema is newer than this flow binary")

// Warnings receives non-fatal migrator notices (backup taken, DB newer
// than this binary but compatible); nil means os.Stderr. Hooks point it
// at io.Discard so their output stays clean.
var Warnings io.Writer

func warnf(format string, a ...any) {
	w := Warnings
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, format, a...)
}

// afterMigrate, when set (tests only), runs once per committed migration
// pass with the from/to versions.
var afterMigrate func(from, to int)

// backupSeq disambiguates backup names taken in the same nanosecond.
var backupSeq atomic.Int64

// keepBackups is how many pre-migration backups survive in backups/.
const keepBackups = 3

type openOptions struct {
	busyTimeoutMS int
	immediateTx   bool
}

// OpenDB opens (or creates) the SQLite database at path and brings its
// schema to latestVersion(). Every pooled connection gets busy_timeout
// (30s) and foreign_keys=ON via the DSN.
func OpenDB(path string) (*sql.DB, error) {
	return openDB(path, openOptions{busyTimeoutMS: 30000})
}

// OpenDBImmediate is OpenDB with `_txlock=immediate`: every db.Begin
// takes the RESERVED lock up front, so concurrent read-modify-write
// transactions (flow do's bootstrap) serialize at Begin instead of
// racing to the first write.
func OpenDBImmediate(path string) (*sql.DB, error) {
	return openDB(path, openOptions{busyTimeoutMS: 30000, immediateTx: true})
}

func openDB(path string, o openOptions) (*sql.DB, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout("+strconv.Itoa(o.busyTimeoutMS)+")")
	q.Add("_pragma", "foreign_keys(1)")
	if o.immediateTx {
		q.Set("_txlock", "immediate")
	}
	db, err := sql.Open("sqlite", "file:"+dsnPath(path)+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if err := migrate(db, path); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// dsnPath escapes the characters a SQLite "file:" URI would otherwise
// interpret: '%' (SQLite decodes %HH), '?' (query start; the driver also
// splits the DSN there) and '#' (fragment). Plain paths are unchanged.
func dsnPath(path string) string {
	return strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(path)
}

func userVersion(q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int, error) {
	var v int
	err := q.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&v)
	return v, err
}

// migrate is the whole per-open schema step.
func migrate(db *sql.DB, path string) error {
	ctx := context.Background()
	v, err := userVersion(db)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	latest := latestVersion()
	if v == latest {
		return nil // fast path: one read, no write lock
	}
	if v > latest {
		return checkNewer(db, v, latest)
	}

	// Slow path. Back up first: VACUUM INTO can't run inside a transaction.
	// Two racing first opens may both back up; that's harmless.
	var tables int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table'`).Scan(&tables); err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}
	// The backup is a safety net, not what keeps the data correct (the
	// migration below is one transaction that rolls back on failure), so
	// a failed backup (full disk, unwritable backups/) warns and goes on
	// rather than leaving flow.db unopenable.
	backup, backupErr := "", error(nil)
	if tables > 0 {
		if backup, backupErr = backupDB(db, path, v); backupErr != nil {
			backup = ""
			warnf("flow: could not back up flow.db before migrating (%v); migrating without a backup\n", backupErr)
		}
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Rebuild migrations need FK enforcement off; the pragma is a no-op
	// inside a transaction, so set it first and restore it before the
	// connection goes back to the pool.
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign_keys: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`) }()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		}
	}()

	// Re-read under the write lock: a concurrent open may have migrated
	// while we waited, in which case there is nothing left to do.
	from, err := userVersion(conn)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if from >= latest {
		committed = true
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		if from > latest {
			return checkNewer(db, from, latest)
		}
		return nil
	}

	q := connQuerier{ctx: ctx, c: conn}
	fkBefore, err := countFKViolations(q)
	if err != nil {
		return err
	}
	for _, m := range migrations[from:] {
		if err := m.Up(q); err != nil {
			return fmt.Errorf("migrate schema v%d (%s): %w", m.Version, m.Name, err)
		}
	}
	fkAfter, err := countFKViolations(q)
	if err != nil {
		return err
	}
	if fkAfter > fkBefore {
		return fmt.Errorf("migrate schema v%d→v%d: foreign_key_check found %d new violation(s)",
			from, latest, fkAfter-fkBefore)
	}
	if _, err := q.Exec(
		`INSERT INTO schema_meta (id, min_binary_schema) VALUES (1, ?)
		 ON CONFLICT(id) DO UPDATE SET min_binary_schema = MAX(min_binary_schema, excluded.min_binary_schema)`,
		compatFloor(migrations)); err != nil {
		return fmt.Errorf("record compat floor: %w", err)
	}
	if _, err := q.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, latest)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	committed = true
	switch {
	case backup != "":
		warnf("flow: migrated flow.db schema v%d → v%d (backup: %s)\n", from, latest, backup)
	case backupErr != nil:
		warnf("flow: migrated flow.db schema v%d → v%d (no backup)\n", from, latest)
	}
	if afterMigrate != nil {
		afterMigrate(from, latest)
	}
	return nil
}

// checkNewer handles a DB migrated by a newer binary: proceed when its
// compat floor still admits this binary, refuse otherwise.
func checkNewer(db *sql.DB, v, latest int) error {
	floor := 0
	err := db.QueryRow(`SELECT min_binary_schema FROM schema_meta WHERE id = 1`).Scan(&floor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) && !strings.Contains(err.Error(), "no such table") {
		return fmt.Errorf("read compat floor: %w", err)
	}
	if floor > latest {
		return fmt.Errorf("%w: flow.db was upgraded by a newer flow (schema v%d, needs a binary at schema v%d or later; this one is v%d); run `flow upgrade`",
			ErrSchemaTooNew, v, floor, latest)
	}
	warnf("flow: flow.db schema v%d is newer than this binary (v%d) but compatible; consider `flow upgrade`\n", v, latest)
	return nil
}

func countFKViolations(q querier) (int, error) {
	rows, err := q.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return 0, fmt.Errorf("foreign_key_check: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	return n, rows.Err()
}

// backupDB writes a consistent copy of the DB to <dir>/backups/ and prunes
// all but the newest keepBackups copies. Returns the backup path.
func backupDB(db *sql.DB, path string, from int) (string, error) {
	dir := filepath.Join(filepath.Dir(path), "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s.bak-v%d-%s-%d-%d", filepath.Base(path), from,
		time.Now().UTC().Format("20060102T150405.000000000Z"), os.Getpid(), backupSeq.Add(1))
	dst := filepath.Join(dir, name)
	// Write under a name pruneBackups ignores, then rename: a concurrent
	// opener's prune must never delete a backup that is still being written.
	tmp := filepath.Join(dir, ".tmp-"+name)
	if _, err := db.Exec(`VACUUM INTO ?`, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	pruneBackups(dir, filepath.Base(path)+".bak-")
	return dst, nil
}

func pruneBackups(dir, prefix string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type bk struct {
		path string
		mod  time.Time
	}
	var bs []bk
	for _, e := range ents {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		bs = append(bs, bk{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	sort.Slice(bs, func(i, j int) bool {
		if !bs[i].mod.Equal(bs[j].mod) {
			return bs[i].mod.After(bs[j].mod)
		}
		return bs[i].path > bs[j].path
	})
	for _, b := range bs[min(len(bs), keepBackups):] {
		_ = os.Remove(b.path)
	}
}

// querier is what migrations run against: *sql.DB in tests, the
// migrator's single connection (via connQuerier) for real.
type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

type connQuerier struct {
	ctx context.Context
	c   *sql.Conn
}

func (q connQuerier) Exec(s string, a ...any) (sql.Result, error) {
	return q.c.ExecContext(q.ctx, s, a...)
}
func (q connQuerier) Query(s string, a ...any) (*sql.Rows, error) {
	return q.c.QueryContext(q.ctx, s, a...)
}
func (q connQuerier) QueryRow(s string, a ...any) *sql.Row {
	return q.c.QueryRowContext(q.ctx, s, a...)
}
