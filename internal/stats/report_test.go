package stats

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"flow/internal/flowdb"
	"flow/internal/harness/claude"
)

func TestBuildStatsEndToEnd(t *testing.T) {
	root := t.TempDir()
	claudeProj := t.TempDir()

	// flow.db with one done task that has a session.
	db, err := flowdb.OpenDB(filepath.Join(root, "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	work := "/work/app"
	mustExec(t, db, `INSERT INTO tasks (slug,name,status,work_dir,session_id,created_at,updated_at)
		VALUES ('t1','T1','done',?, '00000000-0000-4000-8000-000000000001', '2026-06-01T00:00:00Z','2026-06-01T00:00:00Z')`, work)

	// Its jsonl under the encoded cwd path.
	jdir := filepath.Join(claudeProj, claude.EncodeCwd(work))
	os.MkdirAll(jdir, 0o755)
	line := `{"type":"assistant","timestamp":"2026-06-10T10:00:00.000Z","message":{"role":"assistant","usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"content":[{"type":"tool_use","name":"Bash","input":{"command":"flow show task"}}]}}`
	os.WriteFile(filepath.Join(jdir, "00000000-0000-4000-8000-000000000001.jsonl"), []byte(line), 0o644)

	// auto-runs + owner + kb fixtures.
	os.MkdirAll(filepath.Join(root, "tasks", "t1", "auto-runs"), 0o755)
	os.WriteFile(filepath.Join(root, "tasks", "t1", "auto-runs", "r.log"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(root, "owners", "o1", "updates"), 0o755)
	os.WriteFile(filepath.Join(root, "owners", "o1", "updates", "tick.md"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(root, "kb"), 0o755)
	os.WriteFile(filepath.Join(root, "kb", "org.md"), []byte("# org\n- 2026-06-01 — fact one\n- 2026-06-02 — fact two\n"), 0o644)

	c := LoadCache(filepath.Join(root, "stats-cache.json"))
	s, err := BuildStats(BuildOpts{
		Root: root, ClaudeProjects: claudeProj, DB: db, Cache: c,
		Constants: DefaultConstants(), Since: time.Time{}, Project: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.LookupsTotal != 1 || s.LookupsByKind[LookupResume] != 1 {
		t.Errorf("lookups = %d %v, want 1 resume", s.LookupsTotal, s.LookupsByKind)
	}
	if s.Tokens.Total() != 15 {
		t.Errorf("tokens = %d, want 15", s.Tokens.Total())
	}
	if s.TasksDone != 1 || s.AutoRuns != 1 || s.OwnerTicks != 1 || s.KBFacts != 2 {
		t.Errorf("counts done=%d auto=%d ticks=%d kb=%d", s.TasksDone, s.AutoRuns, s.OwnerTicks, s.KBFacts)
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)
	if got, _ := ParseSince("all", now); !got.IsZero() {
		t.Errorf("all → %v, want zero", got)
	}
	if got, _ := ParseSince("7d", now); !got.Equal(now.AddDate(0, 0, -7)) {
		t.Errorf("7d → %v", got)
	}
	if _, err := ParseSince("garbage", now); err == nil {
		t.Errorf("garbage should error")
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}
