package hooksettings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func settingsFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "nested", "settings.json")
}

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse %s: %v (%s)", path, err, raw)
	}
	return m
}

// commandsFor returns the inner command strings recorded under an event.
func commandsFor(t *testing.T, path, event string) []string {
	t.Helper()
	settings := readSettings(t, path)
	hooks, _ := settings["hooks"].(map[string]any)
	entries, _ := hooks[event].([]any)
	var out []string
	for _, e := range entries {
		m, _ := e.(map[string]any)
		inner, _ := m["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if cmd, ok := hm["command"].(string); ok {
				out = append(out, cmd)
			}
		}
	}
	return out
}

func TestInstallCreatesFileAndParentDir(t *testing.T) {
	path := settingsFile(t)

	added, err := Install(path, "SessionStart", "startup|resume", "flow hook session-start")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !added {
		t.Error("Install reported no change on a fresh file")
	}

	settings := readSettings(t, path)
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks key missing: %v", settings)
	}
	entries, _ := hooks["SessionStart"].([]any)
	if len(entries) != 1 {
		t.Fatalf("SessionStart entries = %d, want 1", len(entries))
	}
	entry, _ := entries[0].(map[string]any)
	if got := entry["matcher"]; got != "startup|resume" {
		t.Errorf("matcher = %v, want startup|resume", got)
	}
	inner, _ := entry["hooks"].([]any)
	first, _ := inner[0].(map[string]any)
	if got := first["type"]; got != "command" {
		t.Errorf("type = %v, want command", got)
	}
	if got := first["command"]; got != "flow hook session-start" {
		t.Errorf("command = %v", got)
	}
}

// An event with no matcher must omit the key rather than write "".
func TestInstallOmitsEmptyMatcher(t *testing.T) {
	path := settingsFile(t)
	if _, err := Install(path, "UserPromptSubmit", "", "flow hook user-prompt-submit"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	settings := readSettings(t, path)
	hooks, _ := settings["hooks"].(map[string]any)
	entries, _ := hooks["UserPromptSubmit"].([]any)
	entry, _ := entries[0].(map[string]any)
	if _, present := entry["matcher"]; present {
		t.Errorf("matcher key written for an empty matcher: %v", entry)
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	path := settingsFile(t)
	if _, err := Install(path, "SessionStart", "startup", "flow hook session-start"); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	added, err := Install(path, "SessionStart", "startup", "flow hook session-start")
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if added {
		t.Error("second Install reported a change")
	}
	if got := commandsFor(t, path, "SessionStart"); len(got) != 1 {
		t.Errorf("commands = %v, want exactly one", got)
	}
}

// The whole point of surgical editing: everything flow does not own must
// survive both operations byte-for-byte.
func TestInstallPreservesUnrelatedSettings(t *testing.T) {
	path := settingsFile(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{
	  "model": "opus",
	  "permissions": {"allow": ["Bash(ls:*)"]},
	  "hooks": {
	    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "my-linter"}]}],
	    "SessionStart": [{"matcher": "startup", "hooks": [{"type": "command", "command": "someone-elses-hook"}]}]
	  }
	}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Install(path, "SessionStart", "startup|resume", "flow hook session-start"); err != nil {
		t.Fatalf("Install: %v", err)
	}

	settings := readSettings(t, path)
	if got := settings["model"]; got != "opus" {
		t.Errorf("model = %v, want opus (unrelated key clobbered)", got)
	}
	if _, ok := settings["permissions"]; !ok {
		t.Error("permissions key dropped")
	}
	if got := commandsFor(t, path, "PreToolUse"); len(got) != 1 || got[0] != "my-linter" {
		t.Errorf("PreToolUse commands = %v, want [my-linter]", got)
	}
	sessionStart := commandsFor(t, path, "SessionStart")
	if len(sessionStart) != 2 {
		t.Fatalf("SessionStart commands = %v, want the sibling plus ours", sessionStart)
	}

	// Removing ours must leave the sibling intact.
	removed, err := Uninstall(path, "SessionStart", "flow hook session-start")
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !removed {
		t.Error("Uninstall reported no change")
	}
	if got := commandsFor(t, path, "SessionStart"); len(got) != 1 || got[0] != "someone-elses-hook" {
		t.Errorf("after Uninstall SessionStart = %v, want [someone-elses-hook]", got)
	}
	if got := settings["model"]; got != "opus" {
		t.Errorf("model = %v after Uninstall", got)
	}
}

// Removing the only entry prunes the event and then the hooks map, so we
// don't leave `"hooks": {"SessionStart": []}` litter behind.
func TestUninstallPrunesEmptyContainers(t *testing.T) {
	path := settingsFile(t)
	if _, err := Install(path, "SessionStart", "startup", "flow hook session-start"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := Uninstall(path, "SessionStart", "flow hook session-start"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	settings := readSettings(t, path)
	if _, present := settings["hooks"]; present {
		t.Errorf("empty hooks map left behind: %v", settings)
	}
}

func TestUninstallMissingFileIsNotAnError(t *testing.T) {
	removed, err := Uninstall(filepath.Join(t.TempDir(), "absent.json"), "SessionStart", "flow hook session-start")
	if err != nil {
		t.Errorf("Uninstall on a missing file: %v", err)
	}
	if removed {
		t.Error("Uninstall reported a change with no file")
	}
}

func TestUninstallUnknownCommandIsNoOp(t *testing.T) {
	path := settingsFile(t)
	if _, err := Install(path, "SessionStart", "startup", "flow hook session-start"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := Uninstall(path, "SessionStart", "some-other-command")
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if removed {
		t.Error("Uninstall reported a change for an unmatched command")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("file rewritten despite no change:\n%s\n%s", before, after)
	}
}

// Entries are matched on the trimmed command so a hand-edited
// settings.json with stray whitespace still uninstalls cleanly.
func TestUninstallTrimsRecordedCommand(t *testing.T) {
	path := settingsFile(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"  flow hook session-start  "}]}]}}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	removed, err := Uninstall(path, "SessionStart", "flow hook session-start")
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !removed {
		t.Error("whitespace-padded command was not removed")
	}
}

func TestInstallRejectsMalformedJSON(t *testing.T) {
	path := settingsFile(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(path, "SessionStart", "startup", "cmd"); err == nil {
		t.Error("Install silently accepted malformed settings.json")
	}
	if _, err := Uninstall(path, "SessionStart", "cmd"); err == nil {
		t.Error("Uninstall silently accepted malformed settings.json")
	}
}

// A literal `null` settings file decodes to a nil map; installing into it
// must not panic.
func TestInstallIntoNullDocument(t *testing.T) {
	path := settingsFile(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("null\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(path, "SessionStart", "startup", "flow hook session-start"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got := commandsFor(t, path, "SessionStart"); len(got) != 1 {
		t.Errorf("commands = %v, want one", got)
	}
}

// Two events coexist under one hooks map and are removed independently.
func TestEventsAreIndependent(t *testing.T) {
	path := settingsFile(t)
	if _, err := Install(path, "SessionStart", "startup|resume", "flow hook session-start"); err != nil {
		t.Fatalf("Install SessionStart: %v", err)
	}
	if _, err := Install(path, "UserPromptSubmit", "", "flow hook user-prompt-submit"); err != nil {
		t.Fatalf("Install UserPromptSubmit: %v", err)
	}
	if _, err := Uninstall(path, "SessionStart", "flow hook session-start"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if got := commandsFor(t, path, "UserPromptSubmit"); len(got) != 1 {
		t.Errorf("UserPromptSubmit commands = %v, want it untouched", got)
	}
	if got := commandsFor(t, path, "SessionStart"); len(got) != 0 {
		t.Errorf("SessionStart commands = %v, want none", got)
	}
}

// Hand-written settings can hold shapes we don't produce; skip them
// rather than crashing or silently dropping the user's data.
func TestMalformedEntriesArePreserved(t *testing.T) {
	path := settingsFile(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := `{"hooks":{"SessionStart":[
	  "a bare string entry",
	  {"hooks":["a bare string inner hook",{"type":"command","command":"flow hook session-start"}]}
	]}}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	removed, err := Uninstall(path, "SessionStart", "flow hook session-start")
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !removed {
		t.Fatal("our command was not removed")
	}
	settings := readSettings(t, path)
	hooks, _ := settings["hooks"].(map[string]any)
	entries, _ := hooks["SessionStart"].([]any)
	if len(entries) != 2 {
		t.Errorf("entries = %v, want the bare string and the entry with its surviving inner hook", entries)
	}
}
