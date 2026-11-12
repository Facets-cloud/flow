package flowdb

import (
	"fmt"
	"hash/crc32"
	"path/filepath"
	"testing"
)

// prePeersFingerprint is the user_version a #132-era build (schema
// fingerprint, but no peer hubs) stamped: revision 1 over the three base
// DDL blocks only.
func prePeersFingerprint() int64 {
	h := crc32.NewIEEE()
	fmt.Fprintf(h, "rev=%d\x00", 1)
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

// A DB migrated and stamped by a pre-peers build must not hit OpenDB's
// read-only fast path: migratePeers still has to add the peers table and
// the bus_messages peer columns.
func TestOpenDBMigratesPeersOnPrePeersStampedDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	// Roll back to the pre-peers schema, stamped as a pre-peers build would.
	stmts := []string{
		`DROP INDEX IF EXISTS idx_bus_messages_peer_origin`,
		`DROP TABLE IF EXISTS peers`,
	}
	for _, col := range []string{"peer", "origin_ref", "delivery", "delivery_error", "attachments"} {
		stmts = append(stmts, `ALTER TABLE bus_messages DROP COLUMN `+col)
	}
	stmts = append(stmts, fmt.Sprintf(`PRAGMA user_version = %d`, prePeersFingerprint()))
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	db.Close()

	db, err = OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, col := range []string{"peer", "origin_ref", "delivery", "delivery_error", "attachments"} {
		if has, err := columnExists(db, "bus_messages", col); err != nil || !has {
			t.Errorf("bus_messages.%s missing after open: has=%v err=%v", col, has, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='peers'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("peers table missing after open")
	}
	var v int64
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != schemaFingerprint() {
		t.Errorf("user_version=%d, want %d", v, schemaFingerprint())
	}
}
