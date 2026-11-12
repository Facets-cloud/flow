package flowdb

import (
	"database/sql"
	"fmt"
	"hash/crc32"
	"os"
	"syscall"
	"time"
)

// schemaRevision must be bumped by any migration that does NOT also change
// schemaDDL / busDDL / indexesPostMigrate (e.g. a data-only backfill or a
// table rebuild). DDL changes bump the fingerprint on their own, and so
// do the peer DDL and peerBusColumns.
//
// rev 2: peer hubs (migratePeers). A DB stamped by a pre-peers build must
// not take OpenDB's fast path, or it never gets the peers table and the
// bus_messages peer columns.
const schemaRevision = 2

// schemaFingerprint identifies the schema this binary migrates to. It is
// stored in PRAGMA user_version after a successful migration pass; OpenDB
// skips DDL + migrations (and their write locks) when it matches. Kept
// positive and non-zero: a fresh or pre-fingerprint DB reads 0.
func schemaFingerprint() int64 {
	h := crc32.NewIEEE()
	fmt.Fprintf(h, "rev=%d\x00", schemaRevision)
	for _, ddl := range []string{schemaDDL, busDDL, indexesPostMigrate, peersDDL, peerBusIndexDDL} {
		h.Write([]byte(ddl))
		h.Write([]byte{0})
	}
	for _, col := range peerBusColumns {
		fmt.Fprintf(h, "bus_messages.%s\x00", col)
	}
	v := int64(h.Sum32() & 0x7fffffff)
	if v == 0 {
		v = 1
	}
	return v
}

// schemaIsCurrent is a read-only probe (shared lock only).
func schemaIsCurrent(db *sql.DB) (bool, error) {
	var v int64
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return false, err
	}
	return v == schemaFingerprint(), nil
}

// migrationLockTimeout bounds the wait for the migration lock. A migration
// pass takes well under a second; the bound only exists so an opener
// behind a stuck migrator fails with a message instead of hanging.
var migrationLockTimeout = 2 * time.Minute

// lockMigrations takes an exclusive advisory lock on <db>.migrate.lock so
// concurrent openers (separate processes or goroutines) migrate one at a
// time. Waits up to migrationLockTimeout; the returned func releases it.
func lockMigrations(dbPath string) (func(), error) {
	lockPath := dbPath + ".migrate.lock"
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open migration lock: %w", err)
	}
	deadline := time.Now().Add(migrationLockTimeout)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EINTR && err != syscall.EWOULDBLOCK {
			f.Close()
			return nil, fmt.Errorf("lock migrations: %w", err)
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, fmt.Errorf("lock migrations: timed out after %s waiting for another flow process to finish migrating the database (lock file: %s); retry once it exits",
				migrationLockTimeout, lockPath)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
