package flowdb

import (
	"fmt"
	"hash/crc32"
	"path/filepath"
	"testing"
)

// prLineageFingerprint is the user_version #132's revision-3 build stamped
// (schema fingerprint + peers + harness wake + session_versions), before
// main's session_lineage table (#170/#180) existed.
func prLineageFingerprint() int64 {
	h := crc32.NewIEEE()
	fmt.Fprintf(h, "rev=%d\x00", 3)
	for _, ddl := range []string{schemaDDL, busDDL, indexesPostMigrate, peersDDL, peerBusIndexDDL, wakeDDL, sessionVersionDDL} {
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

// A DB stamped by #132's revision-3 build must not take OpenDB's fast
// path: main's session_lineage table still has to be created, or
// RecordSessionSource fails on every bg resume.
func TestOpenDBMigratesSessionLineageOnPreLineageStampedDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if prLineageFingerprint() == schemaFingerprint() {
		t.Fatalf("schema fingerprint did not change with the session_lineage DDL")
	}
	for _, s := range []string{
		`DROP TABLE IF EXISTS session_lineage`,
		fmt.Sprintf(`PRAGMA user_version = %d`, prLineageFingerprint()),
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
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='session_lineage'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("session_lineage missing after open of a pre-lineage stamped DB")
	}
	if err := RecordSessionSource(db, "child", "parent", "t1"); err != nil {
		t.Fatalf("RecordSessionSource after migration: %v", err)
	}
	var v int64
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != schemaFingerprint() {
		t.Errorf("user_version=%d, want %d", v, schemaFingerprint())
	}
}
