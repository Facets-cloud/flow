package flowdb

import (
	"fmt"
	"hash/crc32"
	"path/filepath"
	"testing"
)

// prWakeFingerprint is the user_version #132's build (schema fingerprint +
// peers, merged before main's harness wake) stamped: revision 2 over the
// base and peer DDL, without the wake / session-version DDL.
func prWakeFingerprint() int64 {
	h := crc32.NewIEEE()
	fmt.Fprintf(h, "rev=%d\x00", 2)
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

// A DB stamped by #132's pre-wake build must not take OpenDB's fast path:
// main's wake tables (#146/#153) and session_versions still have to be
// created, and the alpha.38 bus_surfaced cleanup still has to run.
func TestOpenDBMigratesWakeOnPreWakeStampedDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if prWakeFingerprint() == schemaFingerprint() {
		t.Fatalf("schema fingerprint did not change with the wake/session-version DDL")
	}
	for _, s := range []string{
		`DROP TABLE IF EXISTS bus_wake_nudged`,
		`DROP TABLE IF EXISTS bus_session_state`,
		`DROP TABLE IF EXISTS session_versions`,
		fmt.Sprintf(`PRAGMA user_version = %d`, prWakeFingerprint()),
	} {
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
	for _, tbl := range []string{"bus_wake_nudged", "bus_session_state", "session_versions"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("table %s missing after open of a pre-wake stamped DB", tbl)
		}
	}
	var v int64
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != schemaFingerprint() {
		t.Errorf("user_version=%d, want %d", v, schemaFingerprint())
	}
}
