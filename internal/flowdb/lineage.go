package flowdb

import (
	"database/sql"
	"errors"
	"fmt"
)

// session_lineage records, per harness session id, the session it was
// forked from. flow resumes under the same id whenever claude allows it,
// but `claude --bg --resume <src>` still starts a COPY (`--fork-session`,
// NEW id) when <src> is alive, or was for sessions resumed by older flow
// builds: the history lives in <src>'s transcript until the copy takes a
// turn. If the fork never does (it failed, or was left idle), the task
// is bound to an id with no transcript; the lineage lets flow resume from
// the nearest ancestor that has one instead of starting fresh and losing
// the conversation.
const sessionLineageDDL = `
CREATE TABLE IF NOT EXISTS session_lineage (
    session_id         TEXT PRIMARY KEY,
    source_session_id  TEXT NOT NULL,
    task_slug          TEXT,
    created_at         TEXT NOT NULL
);`

// RecordSessionSource records that sessionID was forked from source.
func RecordSessionSource(db *sql.DB, sessionID, source, taskSlug string) error {
	if sessionID == "" || source == "" {
		return nil
	}
	if sessionID == source {
		return fmt.Errorf("session %s cannot be its own source", sessionID)
	}
	_, err := db.Exec(`INSERT INTO session_lineage (session_id, source_session_id, task_slug, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET source_session_id=excluded.source_session_id,
		  task_slug=excluded.task_slug, created_at=excluded.created_at`,
		sessionID, source, taskSlug, NowISO())
	return err
}

// SessionSource returns the session sessionID was forked from. A missing
// row is ("", false, nil); any other failure is returned.
func SessionSource(db *sql.DB, sessionID string) (string, bool, error) {
	var src string
	err := db.QueryRow(`SELECT source_session_id FROM session_lineage WHERE session_id=?`,
		sessionID).Scan(&src)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return src, true, nil
}
