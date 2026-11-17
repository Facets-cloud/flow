package app

import (
	"crypto/sha256"
	"encoding/json"
	"flow/internal/flowdb"
	"flow/internal/harness/claude"
	"flow/internal/iterm"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// matchFixture builds a temp flow root with tasks on distinct synthetic
// topics, each bound to a fake Claude session whose transcript holds a few
// user prompts plus flow boilerplate the cleaner must drop. Shared by the
// recall CLI tests.
func matchFixture(t *testing.T) (dbPath string, transcript func(slug string) string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("FLOW_ROOT", filepath.Join(tmp, "flow"))
	t.Setenv("HOME", tmp)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	oldOsa := iterm.Runner
	iterm.Runner = func(args []string) error { return nil }
	t.Cleanup(func() { iterm.Runner = oldOsa })
	if rc := cmdInit(nil); rc != 0 {
		t.Fatalf("init rc=%d", rc)
	}
	repo := filepath.Join(tmp, "code", "repo")
	os.MkdirAll(repo, 0o755)
	topics := map[string][]string{
		"kafka-lag":    {"kafka consumer lag on broker partition", "rebalance the kafka consumer group offsets", "kafka broker replica sync", "partition offset reset for consumer", "lag alert on kafka topic"},
		"tf-modules":   {"terraform module variable for provider", "terraform plan shows state drift", "backend state lock in terraform", "module output wiring terraform", "terraform provider version pin"},
		"billing-fix":  {"invoice currency rounding in billing", "refund ledger entry for customer", "billing payment retry on invoice", "tax line on customer invoice", "ledger reconciliation for refunds"},
		"oauth-login":  {"oauth redirect uri mismatch on login", "session cookie scope for oauth token", "refresh token expiry on login", "password reset flow with oauth", "oauth scope consent screen"},
		"grafana-dash": {"grafana panel for latency metric", "alert rule on grafana dashboard", "query latency chart in grafana", "metric panel thresholds dashboard", "grafana dashboard variable for service"},
	}
	dbPath, _ = flowDBPath()
	db, err := flowdb.OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root, _ := flowRoot()
	paths := map[string]string{}
	day := 1
	for slug, prompts := range topics {
		if rc := cmdAdd([]string{"task", "Work on " + slug, "--slug", slug, "--work-dir", repo}); rc != 0 {
			t.Fatalf("add %s rc=%d", slug, rc)
		}
		sid := fakeSessionID(slug)
		ts := fmt.Sprintf("2026-01-%02dT00:00:00Z", day)
		day++
		db.Exec(`UPDATE tasks SET status='in-progress', session_id=?, session_started=?, created_at=? WHERE slug=?`, sid, ts, ts, slug)
		os.WriteFile(filepath.Join(root, "tasks", slug, "brief.md"), []byte("# "+slug+"\n\n"+prompts[0]), 0o644)
		dir := filepath.Join(tmp, ".claude", "projects", claude.EncodeCwd(repo))
		os.MkdirAll(dir, 0o755)
		p := filepath.Join(dir, sid+".jsonl")
		var lines []byte
		add := func(text string) {
			b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}})
			lines = append(lines, append(b, '\n')...)
		}
		add("Base directory for this skill: /x\n# flow skill body kafka terraform invoice oauth grafana")
		for _, pr := range prompts {
			add(pr)
			add("<task-notification>noise</task-notification>")
		}
		os.WriteFile(p, lines, 0o644)
		paths[slug] = p
	}
	return dbPath, func(slug string) string { return paths[slug] }
}

func fileSum(t *testing.T, p string) [32]byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}
