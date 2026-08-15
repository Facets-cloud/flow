package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeManifests points $FLOW_ROOT at a temp dir, drops the given
// manifests into it, and forces a reload. Returns the root.
//
// Reload is required because the registry caches its resolution for the
// process lifetime — the right behaviour in a CLI that runs once, and
// the reason tests must be explicit about invalidating it.
func writeManifests(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ManifestDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FLOW_ROOT", root)
	Reload()
	t.Cleanup(Reload)
	return root
}

const codexManifest = `
schema      = 1
name        = "codex"
binary      = "codex"
session_env = "CODEX_SESSION_ID"

[session]
strategy = "uuid7"
validate = '^[a-z0-9-]+$'

[launch]
argv = ["codex", "--session", "{{.SessionID}}", "{{.Prompt}}"]

[liveness]
probe = "none"

[vocab]
product      = "Codex"
context_file = "AGENTS.md"
`

func names(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, h := range All() {
		out = append(out, string(h.Name()))
	}
	return out
}

// TestManifestAddsHarnessWithoutCodeChange is the whole point of the
// design: a harness flow has never heard of becomes available by
// dropping a file, with no rebuild.
func TestManifestAddsHarnessWithoutCodeChange(t *testing.T) {
	writeManifests(t, map[string]string{"codex.toml": codexManifest})

	h, err := ByName("codex")
	if err != nil {
		t.Fatalf("manifest harness not resolvable: %v", err)
	}
	if got := h.Binary(); got != "codex" {
		t.Errorf("Binary() = %q, want codex", got)
	}
	if got := h.SessionIDEnvVar(); got != "CODEX_SESSION_ID" {
		t.Errorf("SessionIDEnvVar() = %q, want CODEX_SESSION_ID", got)
	}
	if got := h.Vocab().ContextFile; got != "AGENTS.md" {
		t.Errorf("ContextFile = %q, want AGENTS.md", got)
	}
	// Capabilities the manifest did not declare must read as absent.
	if h.Resume() != nil {
		t.Error("Resume() should be nil: the manifest has no [resume]")
	}
	// The built-in must still be there alongside it.
	if _, err := ByName("claude"); err != nil {
		t.Errorf("adding a manifest lost the built-in harness: %v", err)
	}
}

// TestUserManifestShadowsNative pins the precedence rule and, just as
// importantly, that shadowing is REPORTED. Silently replacing a
// built-in would make a stale manifest impossible to diagnose.
func TestUserManifestShadowsNative(t *testing.T) {
	shadow := strings.ReplaceAll(codexManifest, "codex", "claude")
	shadow = strings.Replace(shadow, `session_env = "CLAUDE_SESSION_ID"`, `session_env = "CLAUDE_CODE_SESSION_ID"`, 1)
	writeManifests(t, map[string]string{"claude.toml": shadow})

	h, err := ByName("claude")
	if err != nil {
		t.Fatalf("ByName(claude): %v", err)
	}
	// The manifest version declares no [resume]; the native one does.
	// That difference is how we know which won.
	if h.Resume() != nil {
		t.Error("native claude won; the user manifest should shadow it")
	}
	var shadowReported bool
	for _, err := range Errors() {
		if strings.Contains(err.Error(), "overrides the built-in") {
			shadowReported = true
		}
	}
	if !shadowReported {
		t.Errorf("shadowing a built-in was silent; errors were %v", Errors())
	}
	// Exactly one claude must survive, or callers iterating All() would
	// probe the same harness twice.
	var count int
	for _, n := range names(t) {
		if n == "claude" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("claude appears %d times in All(), want 1", count)
	}
}

// TestBadManifestDisablesOnlyItself: a typo in an experimental manifest
// must not take out the user's working harnesses.
func TestBadManifestDisablesOnlyItself(t *testing.T) {
	writeManifests(t, map[string]string{
		"broken.toml": "schema = 1\nname = \"broken\"\nthis is not toml at all\n",
		"codex.toml":  codexManifest,
	})

	got := names(t)
	for _, want := range []string{"codex", "claude"} {
		if !containsStr(got, want) {
			t.Errorf("harness %q missing after a sibling manifest failed; got %v", want, got)
		}
	}
	if containsStr(got, "broken") {
		t.Errorf("invalid manifest was loaded anyway: %v", got)
	}
	if len(Errors()) == 0 {
		t.Error("invalid manifest produced no error")
	}
}

// TestDuplicateManifestNameRejected: two files claiming the same
// harness must not both register, or resolution becomes order-dependent.
func TestDuplicateManifestNameRejected(t *testing.T) {
	writeManifests(t, map[string]string{
		"a-codex.toml": codexManifest,
		"b-codex.toml": codexManifest,
	})
	var count int
	for _, n := range names(t) {
		if n == "codex" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("codex registered %d times, want 1", count)
	}
	var reported bool
	for _, err := range Errors() {
		if strings.Contains(err.Error(), "already defined") {
			reported = true
		}
	}
	if !reported {
		t.Errorf("duplicate manifest name was silent; errors were %v", Errors())
	}
}

func TestNoManifestDirLeavesNatives(t *testing.T) {
	t.Setenv("FLOW_ROOT", t.TempDir())
	Reload()
	t.Cleanup(Reload)

	if got := names(t); len(got) != 1 || got[0] != "claude" {
		t.Errorf("with no manifests, All() = %v, want [claude]", got)
	}
	if errs := Errors(); len(errs) != 0 {
		t.Errorf("an absent manifest dir should not be an error: %v", errs)
	}
}

// TestAmbientPrefersTheMatchingHarness confirms detection works for a
// manifest-defined harness, not just the built-in.
func TestAmbientPrefersTheMatchingHarness(t *testing.T) {
	writeManifests(t, map[string]string{"codex.toml": codexManifest})
	t.Setenv("CODEX_SESSION_ID", "abc-123")

	h := Ambient()
	if h == nil {
		t.Fatal("Ambient() found nothing with CODEX_SESSION_ID set")
	}
	if got := string(h.Name()); got != "codex" {
		t.Errorf("Ambient() = %q, want codex", got)
	}
}

// TestAmbientRefusesToGuess: with two harnesses' env vars set, picking
// one would silently bind a session to the wrong task.
func TestAmbientRefusesToGuess(t *testing.T) {
	writeManifests(t, map[string]string{"codex.toml": codexManifest})
	t.Setenv("CODEX_SESSION_ID", "abc-123")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "658bf2be-5ae3-4842-a8a4-e0d0b785514d")

	if h := Ambient(); h != nil {
		t.Errorf("Ambient() picked %q with two harnesses claiming the process; it must refuse", h.Name())
	}
}

func containsStr(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
