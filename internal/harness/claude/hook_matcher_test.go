package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readSessionStartEntries(t *testing.T, home string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s.Hooks["SessionStart"]
}

// The SessionStart hook must also fire after compaction: the compacted
// context has lost the listener discipline, and the hook restores it.
func TestSessionStartHookMatcherIncludesCompact(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	h := New()
	if added, err := h.InstallSessionStartHook("flow hook session-start"); err != nil || !added {
		t.Fatalf("install added=%v err=%v", added, err)
	}
	e := readSessionStartEntries(t, home)
	if len(e) != 1 || e[0]["matcher"] != "startup|resume|compact" {
		t.Fatalf("entries = %v, want one with matcher startup|resume|compact", e)
	}
}

// Existing installs carry the old "startup|resume" matcher; re-running
// install migrates it in place (no duplicate entry) and is idempotent.
func TestSessionStartHookMigratesOldMatcher(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"theme":"dark","hooks":{"SessionStart":[
	  {"matcher":"startup","hooks":[{"type":"command","command":"other-tool"}]},
	  {"matcher":"startup|resume","hooks":[{"type":"command","command":"flow hook session-start"}]}
	]}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	h := New()
	if changed, err := h.InstallSessionStartHook("flow hook session-start"); err != nil || !changed {
		t.Fatalf("migrate changed=%v err=%v", changed, err)
	}
	e := readSessionStartEntries(t, home)
	if len(e) != 2 {
		t.Fatalf("got %d SessionStart entries, want 2 (no duplicate): %v", len(e), e)
	}
	if e[0]["matcher"] != "startup" {
		t.Errorf("sibling entry touched: %v", e[0])
	}
	if e[1]["matcher"] != "startup|resume|compact" {
		t.Errorf("flow entry matcher = %v, want startup|resume|compact", e[1]["matcher"])
	}
	if changed, err := h.InstallSessionStartHook("flow hook session-start"); err != nil || changed {
		t.Fatalf("re-run changed=%v err=%v, want idempotent no-op", changed, err)
	}
	if got := len(readSessionStartEntries(t, home)); got != 2 {
		t.Errorf("re-run produced %d entries, want 2", got)
	}
}
