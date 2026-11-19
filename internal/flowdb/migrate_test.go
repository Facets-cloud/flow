package flowdb

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// legacyColumns are columns real DBs carry that current code neither
// creates nor drops: tasks.deleted_at / forked_from_slug from unreleased
// branches, and bus_messages.attempts / next_notify_at from the
// alpha.27 escalation schedule (removed in a later release). The baseline
// keeps them (and their data); they are the only allowed difference
// between a migrated fixture and a fresh DB.
var legacyColumns = map[string]bool{
	"tasks.deleted_at":            true,
	"tasks.forked_from_slug":      true,
	"bus_messages.attempts":       true,
	"bus_messages.next_notify_at": true,
}

func fixtureFiles(t *testing.T) []string {
	t.Helper()
	fs, err := filepath.Glob(filepath.Join("testdata", "schemas", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) < 5 {
		t.Fatalf("expected historical schema fixtures under testdata/schemas, found %d", len(fs))
	}
	return fs
}

// loadFixture replays a fixture dump into a new flow.db (no flow code
// involved) and returns its path.
func loadFixture(t *testing.T, fixture string) string {
	t.Helper()
	b, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "flow.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(string(b)); err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	return path
}

func rawOpen(t *testing.T, path string) *sql.DB {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	return raw
}

// schemaShape is a normalized description of every table (columns with
// type/notnull/default/pk, foreign keys) and index (columns, unique,
// partial), sorted, so two DBs with the same logical schema compare equal
// whatever order their columns were added in. Columns in skip are left out.
func schemaShape(t *testing.T, db *sql.DB, skip map[string]bool) string {
	t.Helper()
	var lines []string
	tables, err := queryStrings(db, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tbl := range tables {
		rows, err := db.Query(`SELECT name, type, "notnull", COALESCE(dflt_value,''), pk FROM pragma_table_info(?)`, tbl)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var name, typ, dflt string
			var nn, pk int
			if err := rows.Scan(&name, &typ, &nn, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			if skip[tbl+"."+name] {
				continue
			}
			lines = append(lines, fmt.Sprintf("col %s.%s %s nn=%d dflt=%s pk=%d", tbl, name, typ, nn, dflt, pk))
		}
		rows.Close()
		rows, err = db.Query(`SELECT "table", "from", COALESCE("to",'') FROM pragma_foreign_key_list(?)`, tbl)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var to, from, toCol string
			if err := rows.Scan(&to, &from, &toCol); err != nil {
				t.Fatal(err)
			}
			lines = append(lines, fmt.Sprintf("fk %s.%s -> %s(%s)", tbl, from, to, toCol))
		}
		rows.Close()
		rows, err = db.Query(`SELECT name, "unique", partial FROM pragma_index_list(?) WHERE origin != 'pk'`, tbl)
		if err != nil {
			t.Fatal(err)
		}
		type idx struct {
			name       string
			uniq, part int
		}
		var idxs []idx
		for rows.Next() {
			var i idx
			if err := rows.Scan(&i.name, &i.uniq, &i.part); err != nil {
				t.Fatal(err)
			}
			idxs = append(idxs, i)
		}
		rows.Close()
		for _, i := range idxs {
			cols, err := queryStrings(db, `SELECT COALESCE(name,'') FROM pragma_index_info(?) ORDER BY seqno`, i.name)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, fmt.Sprintf("idx %s on %s(%s) unique=%d partial=%d",
				i.name, tbl, strings.Join(cols, ","), i.uniq, i.part))
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func freshShape(t *testing.T) string {
	t.Helper()
	db := openTempDB(t)
	return schemaShape(t, db, nil)
}

func tableCounts(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	tables, err := queryStrings(db, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, tbl := range tables {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + quoteIdent(tbl)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[tbl] = n
	}
	return out
}

// taskKeys is every task's identity and user-visible fields, for
// before/after comparison across a migration.
func taskKeys(t *testing.T, db *sql.DB) []string {
	t.Helper()
	keys, err := queryStrings(db, `SELECT slug || '|' || name || '|' || COALESCE(project_slug,'') || '|' ||
		priority || '|' || work_dir || '|' || COALESCE(waiting_on,'') || '|' || created_at || '|' || COALESCE(archived_at,'')
		FROM tasks ORDER BY slug`)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func diffLines(a, b string) string {
	as, bs := map[string]bool{}, map[string]bool{}
	for _, l := range strings.Split(a, "\n") {
		as[l] = true
	}
	for _, l := range strings.Split(b, "\n") {
		bs[l] = true
	}
	var out []string
	for l := range as {
		if !bs[l] {
			out = append(out, "- "+l)
		}
	}
	for l := range bs {
		if !as[l] {
			out = append(out, "+ "+l)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// TestUpgradeFromEveryFixture opens a DB written by every historical
// release (plus hand-made drift shapes) with this binary: it must migrate
// to the latest version, end at exactly the fresh-DB schema, and keep
// every row.
func TestUpgradeFromEveryFixture(t *testing.T) {
	want := freshShape(t)
	for _, fx := range fixtureFiles(t) {
		t.Run(filepath.Base(fx), func(t *testing.T) {
			path := loadFixture(t, fx)
			raw := rawOpen(t, path)
			if v, _ := userVersion(raw); v != 0 {
				t.Fatalf("fixture user_version = %d, want 0 (pre-versioning)", v)
			}
			before := tableCounts(t, raw)
			beforeTasks := taskKeys(t, raw)
			legacyBefore, _ := queryStrings(raw, `SELECT name FROM pragma_table_info('tasks') WHERE name IN ('deleted_at','forked_from_slug') ORDER BY name`)

			db, err := OpenDB(path)
			if err != nil {
				t.Fatalf("OpenDB: %v", err)
			}
			defer db.Close()
			if v, _ := userVersion(db); v != latestVersion() {
				t.Errorf("user_version = %d, want %d", v, latestVersion())
			}
			if got := schemaShape(t, db, legacyColumns); got != want {
				t.Errorf("migrated schema != fresh schema:\n%s", diffLines(want, got))
			}
			legacyAfter, _ := queryStrings(db, `SELECT name FROM pragma_table_info('tasks') WHERE name IN ('deleted_at','forked_from_slug') ORDER BY name`)
			if strings.Join(legacyBefore, ",") != strings.Join(legacyAfter, ",") {
				t.Errorf("legacy columns before %v, after %v", legacyBefore, legacyAfter)
			}
			after := tableCounts(t, db)
			for tbl, n := range before {
				if tbl == "bus_listeners" { // dropped by design (retired feature)
					continue
				}
				if after[tbl] != n {
					t.Errorf("table %s: %d rows before, %d after", tbl, n, after[tbl])
				}
			}
			if got := taskKeys(t, db); strings.Join(got, "\n") != strings.Join(beforeTasks, "\n") {
				t.Errorf("task rows changed:\nbefore %v\nafter  %v", beforeTasks, got)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Err(); err != nil {
				t.Errorf("foreign_key_check: %v", err)
			}
			// The commands' read paths work on the migrated DB.
			if _, err := ListTasks(db, TaskFilter{IncludeArchived: true}); err != nil {
				t.Errorf("ListTasks: %v", err)
			}
			if _, err := ListProjects(db, ProjectFilter{IncludeArchived: true}); err != nil {
				t.Errorf("ListProjects: %v", err)
			}
			// And the second open is the fast path: no migration, no backup.
			db.Close()
			n := 0
			afterMigrate = func(int, int) { n++ }
			defer func() { afterMigrate = nil }()
			db2, err := OpenDB(path)
			if err != nil {
				t.Fatal(err)
			}
			db2.Close()
			if n != 0 {
				t.Errorf("second open migrated again")
			}
		})
	}
}

// Legacy-column data survives the session-invariant table rebuild (the old
// rebuild copied a fixed column list and silently dropped them).
func TestBaselineRebuildKeepsLegacyColumnData(t *testing.T) {
	path := loadFixture(t, filepath.Join("testdata", "schemas", "drift-legacy-columns.sql"))
	want, err := queryStrings(rawOpen(t, path), `SELECT slug || '|' || COALESCE(deleted_at,'') || '|' || COALESCE(parent_slug,'') FROM tasks ORDER BY slug`)
	if err != nil {
		t.Fatal(err)
	}
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ddl string
	_ = db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='tasks'`).Scan(&ddl)
	if !strings.Contains(ddl, "session_id IS NOT NULL") {
		t.Fatalf("fixture should have gone through the invariant rebuild; ddl: %s", ddl)
	}
	got, err := queryStrings(db, `SELECT slug || '|' || COALESCE(deleted_at,'') || '|' || COALESCE(parent_slug,'') FROM tasks ORDER BY slug`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("legacy column data lost:\nwant %v\ngot  %v", want, got)
	}
}

// The data fixes inside the baseline keep their semantics on a drifted DB:
// duplicate session_ids are deduped (newest keeps it) and in-progress rows
// without a session are demoted to backlog.
func TestBaselineDriftDataFixes(t *testing.T) {
	path := loadFixture(t, filepath.Join("testdata", "schemas", "drift-sessions.sql"))
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := queryStrings(db, `SELECT slug || '|' || status || '|' || COALESCE(session_id,'') FROM tasks WHERE slug LIKE 'drift-%' ORDER BY slug`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"drift-dup-new|in-progress|11111111-1111-4111-8111-111111111111",
		"drift-dup-old|backlog|",
		"drift-nosession|backlog|",
	}
	if strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Errorf("drift rows:\ngot  %v\nwant %v", rows, want)
	}
}

func TestFreshDBIsAtLatestVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if v, _ := userVersion(db); v != latestVersion() {
		t.Errorf("user_version = %d, want %d", v, latestVersion())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "backups")); !os.IsNotExist(err) {
		t.Errorf("a fresh DB must not be backed up (stat err %v)", err)
	}
}

// A current DB opens without taking the write lock: another process
// holding RESERVED (BEGIN IMMEDIATE + a write) must not stall the open.
// The pre-versioning OpenDB stalled here until the writer committed.
func TestSecondOpenDoesNoWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	insertProject(t, db, "alpha", "Alpha", "/tmp/alpha", "high")
	db.Close()

	holder := rawOpen(t, path)
	conn, err := holder.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE projects SET name='Held' WHERE slug='alpha'`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(t.Context(), `ROLLBACK`)

	start := time.Now()
	db2, err := openDB(path, openOptions{busyTimeoutMS: 100})
	if err != nil {
		t.Fatalf("open while another connection holds the write lock: %v", err)
	}
	defer db2.Close()
	if p, err := GetProject(db2, "alpha"); err != nil || p.Name != "Alpha" {
		t.Errorf("read under a held write lock: %+v %v", p, err)
	}
	if d := time.Since(start); d > 90*time.Millisecond {
		t.Errorf("open took %v; it should not wait on the writer", d)
	}
}

// Every pooled connection enforces foreign keys (the pragma is in the
// DSN; it used to be a db.Exec that reached one connection only).
func TestForeignKeysOnEveryPooledConnection(t *testing.T) {
	db := openTempDB(t)
	var conns []*sql.Conn
	for range 4 {
		c, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for i, c := range conns {
		var on int
		if err := c.QueryRowContext(t.Context(), `PRAGMA foreign_keys`).Scan(&on); err != nil || on != 1 {
			t.Errorf("conn %d: foreign_keys=%d err=%v", i, on, err)
		}
		c.Close()
	}
}

// Eight processes' worth of first opens on an old DB: all succeed, the
// migration commits exactly once (the rest find it done under the lock).
func TestConcurrentFirstOpensMigrateOnce(t *testing.T) {
	fs := fixtureFiles(t)
	path := loadFixture(t, fs[0])
	var runs atomic.Int32
	afterMigrate = func(int, int) { runs.Add(1) }
	defer func() { afterMigrate = nil }()
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := OpenDB(path)
			if err == nil {
				_, err = ListTasks(db, TaskFilter{})
				db.Close()
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("open %d: %v", i, err)
		}
	}
	if got := runs.Load(); got != 1 {
		t.Errorf("migration committed %d times, want 1", got)
	}
	bks, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backups", "flow.db.bak-*"))
	if len(bks) < 1 || len(bks) > keepBackups {
		t.Errorf("backups = %d, want 1..%d", len(bks), keepBackups)
	}
}

func withMigrations(t *testing.T, extra ...Migration) {
	t.Helper()
	saved := migrations
	migrations = append(append([]Migration{}, saved...), extra...)
	t.Cleanup(func() { migrations = saved })
}

// A migration that fails halfway leaves the DB exactly as it was (DDL is
// transactional; baseline and the failing step share one transaction) and
// the pre-migration backup intact and readable.
func TestMidMigrationFailureLeavesDBAndBackupIntact(t *testing.T) {
	fs := fixtureFiles(t)
	path := loadFixture(t, fs[len(fs)-1])
	raw := rawOpen(t, path)
	beforeShape := schemaShape(t, raw, nil)
	beforeCounts := tableCounts(t, raw)
	raw.Close()

	boom := errors.New("boom")
	withMigrations(t, Migration{
		Version: latestVersion() + 1, Name: "explodes",
		Up: func(q querier) error {
			if _, err := q.Exec(`ALTER TABLE tasks ADD COLUMN half_done TEXT`); err != nil {
				return err
			}
			if _, err := q.Exec(`UPDATE tasks SET name = 'clobbered'`); err != nil {
				return err
			}
			return boom
		},
	})
	if _, err := OpenDB(path); err == nil || !errors.Is(err, boom) {
		t.Fatalf("OpenDB err = %v, want boom", err)
	}

	after := rawOpen(t, path)
	if v, _ := userVersion(after); v != 0 {
		t.Errorf("user_version = %d after failed migration, want 0", v)
	}
	if got := schemaShape(t, after, nil); got != beforeShape {
		t.Errorf("schema changed by failed migration:\n%s", diffLines(beforeShape, got))
	}
	if got := tableCounts(t, after); fmt.Sprint(got) != fmt.Sprint(beforeCounts) {
		t.Errorf("rows changed: %v → %v", beforeCounts, got)
	}
	var clobbered int
	_ = after.QueryRow(`SELECT COUNT(*) FROM tasks WHERE name='clobbered'`).Scan(&clobbered)
	if clobbered != 0 {
		t.Errorf("%d task names clobbered by a rolled-back migration", clobbered)
	}

	bks, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backups", "flow.db.bak-v0-*"))
	if len(bks) != 1 {
		t.Fatalf("backups = %v, want exactly one v0 backup", bks)
	}
	bk := rawOpen(t, bks[0])
	if got := schemaShape(t, bk, nil); got != beforeShape {
		t.Errorf("backup schema differs from original:\n%s", diffLines(beforeShape, got))
	}
	if got := tableCounts(t, bk); fmt.Sprint(got) != fmt.Sprint(beforeCounts) {
		t.Errorf("backup rows %v, original %v", got, beforeCounts)
	}
}

func TestBackupsKeepNewestThree(t *testing.T) {
	fs := fixtureFiles(t)
	path := loadFixture(t, fs[0])
	for i := range 5 {
		db, err := OpenDB(path)
		if err != nil {
			t.Fatal(err)
		}
		resetSchemaVersion(t, db)
		db.Close()
		_ = i
		time.Sleep(10 * time.Millisecond) // distinct mtimes
	}
	bks, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "backups", "flow.db.bak-*"))
	if len(bks) != keepBackups {
		t.Errorf("backups = %d, want %d: %v", len(bks), keepBackups, bks)
	}
}

func setVersionAndFloor(t *testing.T, path string, version, floor int) {
	t.Helper()
	raw := rawOpen(t, path)
	if _, err := raw.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE schema_meta SET min_binary_schema = ?`, floor); err != nil {
		t.Fatal(err)
	}
	raw.Close()
}

func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	saved := Warnings
	Warnings = &buf
	t.Cleanup(func() { Warnings = saved })
	return &buf
}

// A DB migrated by a newer, compatible flow (floor ≤ our latest) opens,
// untouched, with a one-line warning: downgrades keep working.
func TestNewerCompatibleDBOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	insertProject(t, db, "alpha", "Alpha", "/tmp/alpha", "high")
	db.Close()
	setVersionAndFloor(t, path, latestVersion()+3, latestVersion())

	warn := captureWarnings(t)
	db, err = OpenDB(path)
	if err != nil {
		t.Fatalf("newer compatible DB refused: %v", err)
	}
	defer db.Close()
	if _, err := GetProject(db, "alpha"); err != nil {
		t.Errorf("read: %v", err)
	}
	if v, _ := userVersion(db); v != latestVersion()+3 {
		t.Errorf("user_version rewritten to %d", v)
	}
	if !strings.Contains(warn.String(), "newer than this binary") {
		t.Errorf("warning = %q", warn.String())
	}
}

// A DB whose compat floor is above this binary's latest is refused with a
// clear, typed error that tells the user to upgrade.
func TestIncompatibleNewerDBRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	setVersionAndFloor(t, path, latestVersion()+2, latestVersion()+2)

	_, err = OpenDB(path)
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("err = %v, want ErrSchemaTooNew", err)
	}
	if !strings.Contains(err.Error(), "flow upgrade") {
		t.Errorf("error should tell the user to run flow upgrade: %v", err)
	}
}

// A Breaking migration raises the recorded compat floor; non-breaking ones
// leave it alone.
func TestBreakingMigrationRaisesFloor(t *testing.T) {
	floorOf := func(path string) int {
		var f int
		if err := rawOpen(t, path).QueryRow(`SELECT min_binary_schema FROM schema_meta`).Scan(&f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if f := floorOf(path); f != 0 {
		t.Errorf("baseline floor = %d, want 0", f)
	}
	noop := func(querier) error { return nil }
	withMigrations(t,
		Migration{Version: latestVersion() + 1, Name: "additive", Up: noop},
		Migration{Version: latestVersion() + 2, Name: "breaking", Breaking: true, Up: noop},
	)
	db, err = OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if f := floorOf(path); f != latestVersion() {
		t.Errorf("floor = %d, want %d", f, latestVersion())
	}
}

// Bus rows from the 'self' era are rewritten to 'user', and human mail
// that predates seen_at is stamped seen once, when the column lands.
func TestBaselineBusSelfRewriteAndSeenStamp(t *testing.T) {
	path := loadFixture(t, filepath.Join("testdata", "schemas", "drift-bus-self.sql"))
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var self int
	_ = db.QueryRow(`SELECT (SELECT COUNT(*) FROM bus_messages WHERE to_assignee='self' OR from_assignee='self')
		+ (SELECT COUNT(*) FROM bus_watches WHERE watcher='self' OR watcher LIKE 'self/%')`).Scan(&self)
	if self != 0 {
		t.Errorf("%d 'self' rows left", self)
	}
	w, _ := queryStrings(db, `SELECT watcher || '>' || watched FROM bus_watches ORDER BY 1`)
	if got := strings.Join(w, ","); got != "user/t1>alpha,user/t1>t2,user>beta" {
		t.Errorf("watchers = %s", got)
	}
	var human, unseen int
	_ = db.QueryRow(`SELECT COUNT(*), COUNT(*) - COUNT(seen_at) FROM bus_messages
		WHERE to_task_slug IS NULL AND to_assignee='user'`).Scan(&human, &unseen)
	if human != 2 || unseen != 0 {
		t.Errorf("human rows = %d, unseen = %d; want 2 rows, all stamped seen", human, unseen)
	}
}

// TestSeenAtMigrationStampsOnlyLegacyRows (ported from #192, adapted to the
// migrator): eight concurrent first opens of a DB without bus_messages.seen_at,
// each inserting a human ask the moment OpenDB returns. Only the legacy row
// may be stamped seen; a fresh ask must stay unread. The migrator gives this
// structurally — the column add and the stamp share the baseline's one
// BEGIN IMMEDIATE transaction, and losers re-read user_version under the lock,
// so no opener returns before the stamp commits and none re-runs it. The
// same lock is why #192's duplicate-column tolerance has no gap to cover
// here: every column probe runs under the write lock, so no ALTER can lose a
// race, and the opens must all succeed with the migration committed once.
func TestSeenAtMigrationStampsOnlyLegacyRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &BusMessage{ID: "legacy01", CreatedAt: NowISO(), Kind: "message",
		FromAssignee: BusUser, FromTaskSlug: "t", ToAssignee: BusUser, Body: "old ask"}
	if err := insertBusMessage(db, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE bus_messages DROP COLUMN seen_at`); err != nil {
		t.Fatal(err)
	}
	resetSchemaVersion(t, db) // a pre-seen_at DB is pre-versioning
	db.Close()

	var runs atomic.Int32
	afterMigrate = func(int, int) { runs.Add(1) }
	defer func() { afterMigrate = nil }()

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			d, err := OpenDB(path)
			if err != nil {
				errs[i] = err
				return
			}
			defer d.Close()
			errs[i] = insertBusMessage(d, &BusMessage{ID: fmt.Sprintf("new%05d", i), CreatedAt: NowISO(),
				Kind: "message", FromAssignee: BusUser, FromTaskSlug: "t", ToAssignee: BusUser, Body: "new ask"})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	if got := runs.Load(); got != 1 {
		t.Errorf("migration committed %d times, want 1", got)
	}

	db, err = OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var legacySeen sql.NullString
	if err := db.QueryRow(`SELECT seen_at FROM bus_messages WHERE id='legacy01'`).Scan(&legacySeen); err != nil {
		t.Fatal(err)
	}
	if !legacySeen.Valid {
		t.Error("legacy human row was not stamped seen")
	}
	var total, hidden int
	if err := db.QueryRow(`SELECT count(*), count(seen_at) FROM bus_messages WHERE id<>'legacy01'`).Scan(&total, &hidden); err != nil {
		t.Fatal(err)
	}
	if total != n {
		t.Errorf("new asks = %d, want %d", total, n)
	}
	if hidden != 0 {
		t.Errorf("%d asks inserted after OpenDB returned were stamped seen by the backfill", hidden)
	}
}
