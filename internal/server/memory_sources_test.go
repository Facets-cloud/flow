package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUIDataIncludesAgentMemorySources(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)

	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	claudeHome := filepath.Join(home, ".claude")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)

	writeTestFile(t, filepath.Join(codexHome, "AGENTS.override.md"), "# Codex override\ncodex-override-marker\n")
	writeTestFile(t, filepath.Join(codexHome, "AGENTS.md"), "# Codex user memory\ncodex-user-marker\n")
	writeTestFile(t, filepath.Join(codexHome, "memories", "raw_memories.md"), "# Codex raw memory\ncodex-raw-marker\n")
	writeTestFile(t, filepath.Join(codexHome, "memories", "rollout_summaries", "one.md"), "# Codex rollout\ncodex-rollout-marker\n")
	writeTestFile(t, filepath.Join(claudeHome, "CLAUDE.md"), "# Claude user memory\nclaude-user-marker\n")
	writeTestFile(t, filepath.Join(claudeHome, "rules", "workflow.md"), "# Claude user rule\nclaude-user-rule-marker\n")
	writeTestFile(t, filepath.Join(root, "AGENTS.md"), "# Project Codex memory\nproject-codex-marker\n")
	writeTestFile(t, filepath.Join(root, ".claude", "CLAUDE.md"), "# Project Claude memory\nproject-claude-marker\n")
	writeTestFile(t, filepath.Join(root, ".claude", "rules", "testing.md"), "# Project Claude rule\nproject-claude-rule-marker\n")
	writeTestFile(t, filepath.Join(root, "internal", "server", "CLAUDE.md"), "# Nested Claude memory\nnested-claude-marker\n")
	writeTestFile(t, filepath.Join(claudeAutoMemoryDir(root), "MEMORY.md"), "# Claude auto memory\nclaude-auto-marker\n")

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ui-data", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		MemorySources []uiMemorySource `json:"AGENT_MEMORY_SOURCES"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.MemorySources) == 0 {
		t.Fatal("AGENT_MEMORY_SOURCES is empty")
	}

	for marker, want := range map[string]struct {
		provider string
		scope    string
		kind     string
	}{
		"codex-override-marker": {"codex", "user", "instructions"},
		"codex-user-marker":     {"codex", "user", "instructions"},
		"codex-raw-marker":      {"codex", "user", "auto-memory"},
		"codex-rollout-marker":  {"codex", "user", "auto-memory"},
		"project-codex-marker":  {"codex", "project", "instructions"},
		"claude-auto-marker":    {"claude", "project", "auto-memory"},
	} {
		src, ok := findMemorySourceWithContent(payload.MemorySources, marker)
		if !ok {
			t.Fatalf("missing memory marker %q in %#v", marker, payload.MemorySources)
		}
		if !src.Available || src.Status != "available" {
			t.Fatalf("%s availability = %v/%q, want available", marker, src.Available, src.Status)
		}
		if src.Provider != want.provider || src.Scope != want.scope || src.Kind != want.kind {
			t.Fatalf("%s metadata = %+v, want provider=%s scope=%s kind=%s", marker, src, want.provider, want.scope, want.kind)
		}
		if src.Format != "markdown" {
			t.Fatalf("%s format = %q, want markdown", marker, src.Format)
		}
		if src.Path == "" || src.Label == "" {
			t.Fatalf("%s missing path/label metadata: %+v", marker, src)
		}
	}
	for _, marker := range []string{
		"claude-user-marker",
		"claude-user-rule-marker",
		"project-claude-marker",
		"project-claude-rule-marker",
		"nested-claude-marker",
	} {
		if src, ok := findMemorySourceWithContent(payload.MemorySources, marker); ok {
			t.Fatalf("unexpected Claude instruction/rule source for marker %q: %+v", marker, src)
		}
	}
	for _, src := range payload.MemorySources {
		if isClaudeMDPath(src.Path) {
			t.Fatalf("unexpected CLAUDE.md source: %+v", src)
		}
	}
}

func TestUIDataIncludesMissingAgentMemorySources(t *testing.T) {
	root, db := testRootDB(t)
	insertProjectTask(t, db, root)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))

	srv := New(Config{DB: db, FlowRoot: root, Version: "test"}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ui-data", nil)
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		MemorySources []uiMemorySource `json:"AGENT_MEMORY_SOURCES"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{
		filepath.Join(".codex", "AGENTS.override.md"),
		filepath.Join(".codex", "AGENTS.md"),
		filepath.Join(".codex", "memories", "MEMORY.md"),
		filepath.Join(".claude", "projects", claudeProjectKey(root), "memory", "MEMORY.md"),
		filepath.Join(root, "AGENTS.md"),
	} {
		src, ok := findMemorySourceWithPathSuffix(payload.MemorySources, suffix)
		if !ok {
			t.Fatalf("missing unavailable memory source path suffix %q in %#v", suffix, payload.MemorySources)
		}
		if src.Available || src.Status != "missing" || src.Content != "" {
			t.Fatalf("%s = %+v, want missing without content", suffix, src)
		}
	}
}

func TestAgentMemorySourcesKeepDuplicateProjectFilenames(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("CODEX_HOME", filepath.Join(root, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, ".claude"))
	workA := filepath.Join(root, "repo-a")
	workB := filepath.Join(root, "repo-b")
	writeTestFile(t, filepath.Join(workA, "AGENTS.md"), "# Repo A\nrepo-a-marker\n")
	writeTestFile(t, filepath.Join(workB, "AGENTS.md"), "# Repo B\nrepo-b-marker\n")

	srv := &Server{cfg: Config{FlowRoot: root}}
	sources := srv.uiAgentMemorySources(nil, nil, nil, []uiWorkdir{
		{Path: workA, Name: "Repo A"},
		{Path: workB, Name: "Repo B"},
	})

	seenIDs := map[string]bool{}
	markers := map[string]bool{}
	for _, src := range sources {
		if src.Provider != "codex" || src.Scope != "project" || filepath.Base(src.Path) != "AGENTS.md" {
			continue
		}
		if seenIDs[src.ID] {
			t.Fatalf("duplicate memory source id %q in %+v", src.ID, sources)
		}
		seenIDs[src.ID] = true
		if strings.Contains(src.Content, "repo-a-marker") {
			markers["repo-a-marker"] = true
		}
		if strings.Contains(src.Content, "repo-b-marker") {
			markers["repo-b-marker"] = true
		}
	}
	if !markers["repo-a-marker"] || !markers["repo-b-marker"] {
		t.Fatalf("project memory sources collapsed same-named files: %+v", sources)
	}
}

func findMemorySourceWithContent(sources []uiMemorySource, marker string) (uiMemorySource, bool) {
	for _, src := range sources {
		if strings.Contains(src.Content, marker) {
			return src, true
		}
	}
	return uiMemorySource{}, false
}

func findMemorySourceWithPathSuffix(sources []uiMemorySource, suffix string) (uiMemorySource, bool) {
	suffix = filepath.Clean(suffix)
	for _, src := range sources {
		if strings.HasSuffix(filepath.Clean(src.Path), suffix) {
			return src, true
		}
	}
	return uiMemorySource{}, false
}

func writeTestFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
