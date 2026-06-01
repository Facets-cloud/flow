package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCmdSearchFindsBriefsAndUpdates(t *testing.T) {
	root := setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"task", "Build UI", "--slug", "build-ui", "--work-dir", wd, "--agent", "claude"}); rc != 0 {
		t.Fatalf("cmdAdd task rc=%d", rc)
	}
	if err := os.WriteFile(filepath.Join(root, "tasks", "build-ui", "brief.md"), []byte("brief search marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tasks", "build-ui", "updates", "2026-05-20-progress.md"), []byte("update search marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if rc := cmdSearch([]string{"brief search marker"}); rc != 0 {
			t.Fatalf("cmdSearch brief rc=%d", rc)
		}
	})
	if !strings.Contains(out, "task_brief") || !strings.Contains(out, "build-ui") {
		t.Fatalf("brief output = %s", out)
	}
	out = captureStdout(t, func() {
		if rc := cmdSearch([]string{"update search marker"}); rc != 0 {
			t.Fatalf("cmdSearch update rc=%d", rc)
		}
	})
	if !strings.Contains(out, "task_update") || !strings.Contains(out, "build-ui") {
		t.Fatalf("update output = %s", out)
	}
}

func TestCmdSearchTranscriptsAreOptIn(t *testing.T) {
	root := setupFlowRoot(t)
	wd := t.TempDir()
	if rc := cmdAdd([]string{"task", "Investigate", "--slug", "investigate", "--work-dir", wd, "--agent", "claude"}); rc != 0 {
		t.Fatalf("cmdAdd task rc=%d", rc)
	}
	transcriptPath := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"user","message":{"content":"transcript search marker"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET session_path = ? WHERE slug = 'investigate'`, transcriptPath); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if rc := cmdSearch([]string{"transcript search marker"}); rc != 0 {
			t.Fatalf("cmdSearch default rc=%d", rc)
		}
	})
	if strings.Contains(out, "task_transcript") || strings.Contains(out, "investigate") {
		t.Fatalf("default search included transcript: %s", out)
	}

	out = captureStdout(t, func() {
		if rc := cmdSearch([]string{"transcript search marker", "--in", "transcripts"}); rc != 0 {
			t.Fatalf("cmdSearch transcripts rc=%d", rc)
		}
	})
	if !strings.Contains(out, "task_transcript") || !strings.Contains(out, "investigate") {
		t.Fatalf("transcript output = %s", out)
	}
}

func TestCmdSearchFindsMemoriesByDefault(t *testing.T) {
	root := setupFlowRoot(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	if err := os.WriteFile(filepath.Join(root, "kb", "user.md"), []byte("cli-flow-memory-marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex", "memories"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "memories", "raw_memories.md"), []byte("cli-codex-memory-marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if rc := cmdSearch([]string{"cli-flow-memory-marker"}); rc != 0 {
			t.Fatalf("cmdSearch flow memory rc=%d", rc)
		}
	})
	if !strings.Contains(out, "memory") || !strings.Contains(out, "flow-kb-user") {
		t.Fatalf("flow memory output = %s", out)
	}

	out = captureStdout(t, func() {
		if rc := cmdSearch([]string{"cli-codex-memory-marker", "--in", "memories"}); rc != 0 {
			t.Fatalf("cmdSearch codex memory rc=%d", rc)
		}
	})
	if !strings.Contains(out, "memory") || !strings.Contains(out, "codex-memory-raw-memories") {
		t.Fatalf("codex memory output = %s", out)
	}
}
