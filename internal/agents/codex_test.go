package agents

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flow/internal/flowdb"
)

func TestCaptureCodexSessionForTaskSince(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	workDir := t.TempDir()
	sessionDir := filepath.Join(codexHome, "sessions", "2026", "05", "15")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionID := "55555555-5555-4555-8555-555555555555"
	jsonl := `{"type":"session_meta","payload":{"id":"` + sessionID + `","cwd":"` + workDir + `","timestamp":"2026-05-15T10:00:00Z"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"flow task codex-task"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionDir, sessionID+".jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := flowdb.OpenDB(filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := flowdb.NowISO()
	if _, err := db.Exec(
		`INSERT INTO tasks (slug, name, status, priority, work_dir, session_provider, session_started, created_at, updated_at)
		 VALUES ('codex-task', 'Codex Task', 'in-progress', 'medium', ?, 'codex', ?, ?, ?)`,
		workDir, now, now, now,
	); err != nil {
		t.Fatal(err)
	}

	captured, err := CaptureCodexSessionForTaskSince(db, "codex-task", workDir, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if captured != sessionID {
		t.Fatalf("captured = %q, want %q", captured, sessionID)
	}
	var got sql.NullString
	if err := db.QueryRow(`SELECT session_id FROM tasks WHERE slug = 'codex-task'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.String != sessionID {
		t.Fatalf("stored session_id = %+v, want %s", got, sessionID)
	}
}
