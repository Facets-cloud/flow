package flowdb

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

const concurrentOpenHelperEnv = "FLOWDB_CONCURRENT_OPEN_HELPER"

// TestHelperConcurrentOpen is not a test: it is the child body for
// TestOpenDBConcurrentProcesses (re-executed test binary). It opens the DB
// repeatedly and does one small write per open, the way every flow command
// (and every parallel `flow auto` supervisor) does.
func TestHelperConcurrentOpen(t *testing.T) {
	path := os.Getenv(concurrentOpenHelperEnv)
	if path == "" {
		t.Skip("helper process only")
	}
	iters, _ := strconv.Atoi(os.Getenv("FLOWDB_CONCURRENT_OPEN_ITERS"))
	for i := 0; i < iters; i++ {
		db, err := OpenDB(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "OPEN-ERROR: %v\n", err)
			os.Exit(1)
		}
		if _, err := db.Exec(`UPDATE workdirs SET last_used_at=? WHERE path='/nope'`, NowISO()); err != nil {
			fmt.Fprintf(os.Stderr, "WRITE-ERROR: %v\n", err)
			os.Exit(1)
		}
		db.Close()
	}
}

// F4: two `flow auto` launched at once killed one supervisor with
// "migrate bus assignee: database is locked (5) (SQLITE_BUSY)". DB open +
// migrations must be safe under many concurrent processes.
func TestOpenDBConcurrentProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	const procs, iters = 12, 15
	var wg sync.WaitGroup
	outs := make([][]byte, procs)
	errs := make([]error, procs)
	for i := 0; i < procs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperConcurrentOpen$", "-test.count=1")
			cmd.Env = append(os.Environ(), concurrentOpenHelperEnv+"="+path, "FLOWDB_CONCURRENT_OPEN_ITERS="+strconv.Itoa(iters))
			outs[i], errs[i] = cmd.CombinedOutput()
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Errorf("proc %d: %v\n%s", i, errs[i], outs[i])
		}
	}
}

// F4 root cause, part 1: every OpenDB ran write migrations (e.g. the
// unconditional bus 'self'->'user' UPDATEs), so a plain open had to wait
// for any writer — and a sibling `flow do` held its write lock across a
// slow Codex probe. Opening an already-current DB must not need a write
// lock at all.
func TestOpenDBCurrentSchemaNeedsNoWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Another process holds the write lock (RESERVED) and doesn't let go.
	holder, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	holder.SetMaxOpenConns(1)
	if _, err := holder.Exec(`BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer holder.Exec(`ROLLBACK`)
	if _, err := holder.Exec(`UPDATE workdirs SET last_used_at='x' WHERE path='/nope'`); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		db, err := OpenDB(path)
		if err == nil {
			// reads still work under a RESERVED lock
			var n int
			err = db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&n)
			db.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("OpenDB under a held write lock: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OpenDB blocked on a write lock for an already-current schema")
	}
}

// Migrations are serialized and run once: N processes opening a DB that
// predates the fingerprint (user_version 0, missing columns) all succeed,
// and the DB ends at the current version.
func TestOpenDBConcurrentProcessesMigrateOldDB(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	// Roll the DB back to "old": drop the fingerprint and a migrated column.
	if _, err := db.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE bus_messages DROP COLUMN seen_at`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	const procs = 10
	var wg sync.WaitGroup
	outs := make([][]byte, procs)
	errs := make([]error, procs)
	for i := 0; i < procs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperConcurrentOpen$", "-test.count=1")
			cmd.Env = append(os.Environ(), concurrentOpenHelperEnv+"="+path, "FLOWDB_CONCURRENT_OPEN_ITERS=3")
			outs[i], errs[i] = cmd.CombinedOutput()
		}(i)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Errorf("proc %d: %v\n%s", i, errs[i], outs[i])
		}
	}
	db, err = OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if has, err := columnExists(db, "bus_messages", "seen_at"); err != nil || !has {
		t.Errorf("seen_at not migrated back: has=%v err=%v", has, err)
	}
	var v int64
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != schemaFingerprint() {
		t.Errorf("user_version=%d, want fingerprint %d", v, schemaFingerprint())
	}
}

// markPreFingerprint makes a test DB look like one written before the
// schema fingerprint existed (user_version 0), so the next OpenDB runs
// the full migration pass — as it does for real old DBs.
func markPreFingerprint(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`PRAGMA user_version = 0`); err != nil {
		t.Fatal(err)
	}
}
