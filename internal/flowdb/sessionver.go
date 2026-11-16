package flowdb

import (
	"database/sql"
	"errors"
	"time"
)

// Upgrade notice state (see internal/app/upgradenotice.go).
//
// session_versions records, per harness session id, the flow version the
// session last saw. SessionStart records it silently; the prompt and tool
// hooks compare-and-set it, and only the hook call whose CAS changed the
// row tells the session that flow was upgraded under it — once per change.
// Keyed by session, not task: unbound sessions load the skill too.
const sessionVersionDDL = `
CREATE TABLE IF NOT EXISTS session_versions (
    session_id    TEXT PRIMARY KEY,
    seen_version  TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);`

// SessionVersionTTL bounds the table: a row nobody touched for this long
// is pruned. A session silent that long that wakes up again is told about
// the upgrade as "from an earlier version", which is still true.
const SessionVersionTTL = 90 * 24 * time.Hour

func sessionVersionTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// RecordSessionVersion sets the session's seen version without a claim
// (SessionStart: the session has just loaded the current skill), and
// prunes rows older than SessionVersionTTL.
func RecordSessionVersion(db *sql.DB, sessionID, version string, now time.Time) error {
	if _, err := db.Exec(`INSERT INTO session_versions (session_id, seen_version, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET seen_version=excluded.seen_version, updated_at=excluded.updated_at`,
		sessionID, version, sessionVersionTime(now)); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM session_versions WHERE updated_at < ?`,
		sessionVersionTime(now.Add(-SessionVersionTTL)))
	return err
}

// ClaimSessionVersion atomically moves the session's seen version to
// version. won is true only for the single caller whose write changed the
// row (concurrent hooks race; the losers see won=false). old is the
// version the session saw before; known is false when the session had no
// row at all (it started on a flow that predates this record).
func ClaimSessionVersion(db *sql.DB, sessionID, version string, now time.Time) (old string, known, won bool, err error) {
	err = db.QueryRow(`SELECT seen_version FROM session_versions WHERE session_id=?`, sessionID).Scan(&old)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := db.Exec(`INSERT OR IGNORE INTO session_versions (session_id, seen_version, updated_at)
			VALUES (?, ?, ?)`, sessionID, version, sessionVersionTime(now))
		if err != nil {
			return "", false, false, err
		}
		n, _ := res.RowsAffected()
		return "", false, n == 1, nil
	case err != nil:
		return "", false, false, err
	}
	if old == version {
		return old, true, false, nil
	}
	// Compare-and-set on the value read: of several hooks that read the
	// same old version, exactly one update matches.
	res, err := db.Exec(`UPDATE session_versions SET seen_version=?, updated_at=?
		WHERE session_id=? AND seen_version=?`, version, sessionVersionTime(now), sessionID, old)
	if err != nil {
		return old, true, false, err
	}
	n, _ := res.RowsAffected()
	return old, true, n == 1, nil
}
