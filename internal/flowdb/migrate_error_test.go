package flowdb

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// Review note 5 on #132: the bus column backfills used `err == nil &&
// !has`, so a columnExists error skipped the ALTER silently and OpenDB
// stamped the schema fingerprint anyway. With the fast path nothing
// retries a stamped DB, so the skipped migration became permanent. The
// error must fail the open and leave the DB unstamped.
func TestOpenDBDoesNotStampWhenColumnProbeFails(t *testing.T) {
	for _, tc := range []struct{ table, col string }{
		{"bus_nudges", "attempts"},
		{"bus_messages", "reply_to"},
		{"bus_messages", "consumed_by_session"},
		{"bus_messages", "notified_at"},
		{"bus_messages", "seen_at"},
	} {
		t.Run(tc.table+"."+tc.col, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "flow.db")
			db, err := OpenDB(path)
			if err != nil {
				t.Fatal(err)
			}
			// An older DB: the column is missing and nothing is stamped.
			for _, s := range []string{
				`ALTER TABLE ` + tc.table + ` DROP COLUMN ` + tc.col,
				`PRAGMA user_version = 0`,
			} {
				if _, err := db.Exec(s); err != nil {
					t.Fatalf("%s: %v", s, err)
				}
			}
			db.Close()

			injected := errors.New("injected probe failure")
			orig := columnExists
			columnExists = func(db *sql.DB, table, column string) (bool, error) {
				if table == tc.table && column == tc.col {
					return false, injected
				}
				return orig(db, table, column)
			}
			t.Cleanup(func() { columnExists = orig })

			if db, err := OpenDB(path); err == nil {
				db.Close()
				t.Errorf("OpenDB succeeded although the %s.%s probe failed", tc.table, tc.col)
			} else if !errors.Is(err, injected) {
				t.Errorf("OpenDB error = %v, want it to wrap the probe failure", err)
			}

			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			current, err := schemaIsCurrent(raw)
			raw.Close()
			if err != nil {
				t.Fatal(err)
			}
			if current {
				t.Fatalf("schema stamped as current although the %s.%s migration was skipped", tc.table, tc.col)
			}

			// The next open (probe healthy again) retries and finishes.
			columnExists = orig
			db, err = OpenDB(path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if has, err := columnExists(db, tc.table, tc.col); err != nil || !has {
				t.Errorf("%s.%s missing after the retry: has=%v err=%v", tc.table, tc.col, has, err)
			}
			if current, err := schemaIsCurrent(db); err != nil || !current {
				t.Errorf("schema not stamped after a clean migration: current=%v err=%v", current, err)
			}
		})
	}
}
