package flowdb

import (
	"database/sql"
	"fmt"
	"hash/crc32"
	"os"
	"syscall"
)

// schemaRevision must be bumped by any migration that does NOT also change
// schemaDDL / busDDL / indexesPostMigrate (e.g. a data-only backfill or a
// table rebuild). DDL changes bump the fingerprint on their own.
const schemaRevision = 1

// schemaFingerprint identifies the schema this binary migrates to. It is
// stored in PRAGMA user_version after a successful migration pass; OpenDB
// skips DDL + migrations (and their write locks) when it matches. Kept
// positive and non-zero: a fresh or pre-fingerprint DB reads 0.
func schemaFingerprint() int64 {
	h := crc32.NewIEEE()
	fmt.Fprintf(h, "rev=%d\x00", schemaRevision)
	for _, ddl := range []string{schemaDDL, busDDL, indexesPostMigrate} {
		h.Write([]byte(ddl))
		h.Write([]byte{0})
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

// lockMigrations takes an exclusive advisory lock on <db>.migrate.lock so
// concurrent openers (separate processes or goroutines) migrate one at a
// time. Blocks until acquired; the returned func releases it.
func lockMigrations(dbPath string) (func(), error) {
	f, err := os.OpenFile(dbPath+".migrate.lock", os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open migration lock: %w", err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("lock migrations: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
