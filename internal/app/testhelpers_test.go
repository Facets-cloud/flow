package app

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	flowdb "flow/internal/flowdb"
)

func openTempDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flow.db")
	db, err := flowdb.OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func insertProject(t *testing.T, db *sql.DB, slug, name, wd, priority string) {
	t.Helper()
	now := flowdb.NowISO()
	_, err := db.Exec(`INSERT INTO projects (slug, name, status, priority, work_dir, created_at, updated_at) VALUES (?, ?, 'active', ?, ?, ?, ?)`,
		slug, name, priority, wd, now, now)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
}

func insertTask(t *testing.T, db *sql.DB, slug, name, status, priority, wd string, project any) {
	t.Helper()
	now := flowdb.NowISO()
	// Session-id invariant: only backlog may have a NULL session_id.
	// Tests that create non-backlog rows get a deterministic per-slug
	// placeholder UUID — unique (so the partial unique index is happy)
	// without needing real Claude session bookkeeping.
	var sessionID any
	if status != "backlog" {
		sessionID = fakeSessionID(slug)
	}
	_, err := db.Exec(
		`INSERT INTO tasks (slug, name, project_slug, status, priority, work_dir, session_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		slug, name, project, status, priority, wd, sessionID, now, now,
	)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}
}

// fakeSessionID makes a deterministic v4-shaped UUID derived from
// slug. Used by insertTask to satisfy the session-id invariant on
// non-backlog rows without entangling tests with real Claude
// session lifecycles. The format passes sessionUUIDRe so tests that
// pin --session-id-style behavior keep working.
func fakeSessionID(slug string) string {
	// FNV-64 hash → 16 hex chars, padded to 32 for v4 layout.
	const fnvOff = 0xcbf29ce484222325
	const fnvPri = 0x100000001b3
	h := uint64(fnvOff)
	for _, b := range []byte(slug) {
		h ^= uint64(b)
		h *= fnvPri
	}
	// Build "xxxxxxxx-xxxx-4xxx-8xxx-xxxxxxxxxxxx" from one hash + slug filler.
	first := fmt.Sprintf("%016x", h)
	// Use the slug bytes (zero-padded) as additional entropy for the
	// last 12 chars so distinct slugs that hash-collide still differ.
	pad := slug
	for len(pad) < 12 {
		pad += "0"
	}
	tailRaw := []byte(pad)[:12]
	for i, b := range tailRaw {
		// Force into hex range.
		tailRaw[i] = "0123456789abcdef"[uint(b)%16]
	}
	tail := string(tailRaw)
	return fmt.Sprintf("%s-%s-4%s-8%s-%s", first[:8], first[8:12], first[12:15], first[15:16]+first[0:2], tail)
}
