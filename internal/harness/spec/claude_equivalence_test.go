package spec_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"flow/internal/harness"
	"flow/internal/harness/claude"
	"flow/internal/harness/spec"
)

// loadClaudeSpec builds an adapter from the reference manifest.
func loadClaudeSpec(t *testing.T) *spec.Adapter {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "claude.toml"))
	if err != nil {
		t.Fatalf("read reference manifest: %v", err)
	}
	s, err := spec.Decode(data, "testdata/claude.toml")
	if err != nil {
		t.Fatalf("decode reference manifest: %v", err)
	}
	a, err := spec.New(s)
	if err != nil {
		t.Fatalf("build adapter: %v", err)
	}
	return a
}

// TestClaudeSpecMatchesNativeAdapter is the honesty check on the whole
// manifest design: the declarative engine, driven by testdata/claude.toml,
// must produce output BYTE-IDENTICAL to the hand-written Go adapter for
// every command flow builds.
//
// Byte-identity rather than mere shell-equivalence is deliberate. It is
// the difference between "the DSL can express claude" and "the DSL can
// express something that also works" — only the former proves a second
// harness will not need a code change. It is also what catches quoting
// drift: `claude --session-id X 'p'` and `'claude' '--session-id' 'X' 'p'`
// behave the same but mean the design lost the distinction between a
// literal token and user data.
func TestClaudeSpecMatchesNativeAdapter(t *testing.T) {
	native := claude.New()
	generated := loadClaudeSpec(t)

	const sid = "658bf2be-5ae3-4842-a8a4-e0d0b785514d"

	prompts := map[string]string{
		"plain":        "do the thing",
		"single quote": "don't stop",
		"double quote": `say "hello"`,
		"subshell":     "run $(rm -rf /) now",
		"backtick":     "run `whoami` now",
		"newlines":     "line one\nline two\n\nline four",
		"unicode":      "ship it 🚀 — em-dash and all",
		"empty":        "",
	}
	injects := map[string]string{
		"none":      "",
		"simple":    "read the spec first",
		"quoted":    "read 'the spec' first",
		"multiline": "step one\nstep two",
	}

	for pName, prompt := range prompts {
		for iName, inject := range injects {
			for _, skip := range []bool{false, true} {
				opts := harness.LaunchOpts{Inject: inject, SkipPermissions: skip}
				label := pName + "/" + iName
				if skip {
					label += "/skip-perms"
				}

				t.Run("LaunchCmd/"+label, func(t *testing.T) {
					want := native.LaunchCmd(sid, prompt, opts)
					got := generated.LaunchCmd(sid, prompt, opts)
					if got != want {
						t.Errorf("manifest and native adapter disagree\n native: %q\n  spec:  %q", want, got)
					}
				})

				t.Run("ResumeCmd/"+label, func(t *testing.T) {
					want := native.Resume().ResumeCmd(sid, opts)
					got := generated.Resume().ResumeCmd(sid, opts)
					if got != want {
						t.Errorf("manifest and native adapter disagree\n native: %q\n  spec:  %q", want, got)
					}
				})

				t.Run("AutoRunArgv/"+label, func(t *testing.T) {
					want := native.Headless().AutoRunArgv(sid, prompt, opts)
					got := generated.Headless().AutoRunArgv(sid, prompt, opts)
					if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
						t.Errorf("manifest and native adapter disagree\n native: %q\n  spec:  %q", want, got)
					}
				})
			}
		}
	}
}

// TestClaudeSpecMatchesNativeIdentity covers the non-command surface:
// the strings and predicates other code branches on.
func TestClaudeSpecMatchesNativeIdentity(t *testing.T) {
	native := claude.New()
	generated := loadClaudeSpec(t)

	if got, want := string(generated.Name()), string(native.Name()); got != want {
		t.Errorf("Name: spec %q, native %q", got, want)
	}
	if got, want := generated.Binary(), native.Binary(); got != want {
		t.Errorf("Binary: spec %q, native %q", got, want)
	}
	if got, want := generated.SessionIDEnvVar(), native.SessionIDEnvVar(); got != want {
		t.Errorf("SessionIDEnvVar: spec %q, native %q", got, want)
	}
	if got, want := generated.Vocab(), native.Vocab(); got != want {
		t.Errorf("Vocab: spec %+v, native %+v", got, want)
	}
}

// TestClaudeSpecSessionIDValidationMatches pins the id regexp. A
// mismatch here would let one adapter accept a session id the other
// rejects — the kind of divergence that only shows up when a real
// session fails to bind.
func TestClaudeSpecSessionIDValidationMatches(t *testing.T) {
	native := claude.New()
	generated := loadClaudeSpec(t)

	ids := []string{
		"658bf2be-5ae3-4842-a8a4-e0d0b785514d", // canonical v4
		"658BF2BE-5AE3-4842-A8A4-E0D0B785514D", // uppercase
		"658bf2be-5ae3-1842-a8a4-e0d0b785514d", // wrong version
		"658bf2be-5ae3-4842-c8a4-e0d0b785514d", // wrong variant
		"not-a-uuid",
		"",
		"658bf2be-5ae3-4842-a8a4-e0d0b785514d extra",
	}
	for _, id := range ids {
		nativeErr := native.ValidateSessionID(id) != nil
		specErr := generated.ValidateSessionID(id) != nil
		if nativeErr != specErr {
			t.Errorf("ValidateSessionID(%q): native rejects=%v, spec rejects=%v", id, nativeErr, specErr)
		}
	}
}

// TestClaudeSpecLivenessMatchesNative feeds both adapters the same fake
// process table and requires the same id→count map.
func TestClaudeSpecLivenessMatchesNative(t *testing.T) {
	const psOutput = `  PID COMMAND
  101 claude --session-id 658bf2be-5ae3-4842-a8a4-e0d0b785514d
  102 claude --resume 11111111-2222-4333-8444-555555555555
  103 /usr/local/bin/claude --session-id=99999999-8888-4777-b666-555555555555
  104 vim notes.md
  105 claude --session-id 658bf2be-5ae3-4842-a8a4-e0d0b785514d
  106 claude --session-id not-a-uuid
`
	nativePS := claude.PSRunner
	specPS := spec.PSRunner
	claude.PSRunner = func() ([]byte, error) { return []byte(psOutput), nil }
	spec.PSRunner = func() ([]byte, error) { return []byte(psOutput), nil }
	t.Cleanup(func() {
		claude.PSRunner = nativePS
		spec.PSRunner = specPS
	})

	want, err := claude.New().LiveSessionIDs()
	if err != nil {
		t.Fatalf("native LiveSessionIDs: %v", err)
	}
	got, err := loadClaudeSpec(t).LiveSessionIDs()
	if err != nil {
		t.Fatalf("spec LiveSessionIDs: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("live session counts differ\n native: %v\n  spec:  %v", want, got)
	}
	for id, n := range want {
		if got[id] != n {
			t.Errorf("session %s: native count %d, spec count %d", id, n, got[id])
		}
	}
	if len(want) == 0 {
		t.Fatal("fixture produced no matches — the test would pass vacuously")
	}
}

// skillFixture is a minimal skill tree with the same shape as flow's
// real one: SKILL.md at the root, references/ beside it.
func skillFixture() fstest.MapFS {
	return fstest.MapFS{
		"SKILL.md":            &fstest.MapFile{Data: []byte("---\nname: flow\ndescription: task manager\n---\n\nbody\n")},
		"references/setup.md": &fstest.MapFile{Data: []byte("setup reference\n")},
	}
}

// TestClaudeSpecSkillInstallMatchesNative installs the same skill tree
// through both adapters into separate temp homes and requires the
// resulting directories to be identical.
//
// Skill install is the capability a user notices immediately when it
// diverges, and it writes to a path they already have real content in.
func TestClaudeSpecSkillInstallMatchesNative(t *testing.T) {
	nativeHome := t.TempDir()
	specHome := t.TempDir()

	t.Setenv("HOME", nativeHome)
	if err := claude.New().Skills().InstallSkill(skillFixture()); err != nil {
		t.Fatalf("native InstallSkill: %v", err)
	}
	nativePath, err := claude.New().Skills().SkillInstallPath()
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", specHome)
	generated := loadClaudeSpec(t)
	if err := generated.Skills().InstallSkill(skillFixture()); err != nil {
		t.Fatalf("spec InstallSkill: %v", err)
	}
	specPath, err := generated.Skills().SkillInstallPath()
	if err != nil {
		t.Fatal(err)
	}

	if rel(t, nativePath, nativeHome) != rel(t, specPath, specHome) {
		t.Errorf("SkillInstallPath differs\n native: %s\n  spec:  %s",
			rel(t, nativePath, nativeHome), rel(t, specPath, specHome))
	}
	assertTreesEqual(t, filepath.Dir(nativePath), filepath.Dir(specPath))
}

// TestClaudeSpecHookInstallMatchesNative requires the settings.json
// written by the manifest to be byte-identical to the native adapter's.
//
// This is the strictest test in the suite: it covers entry shape, key
// ordering, indentation and the trailing newline all at once, on a file
// the user's harness will refuse to start without if it is malformed.
func TestClaudeSpecHookInstallMatchesNative(t *testing.T) {
	const (
		sessionCmd = "flow hook session-start"
		promptCmd  = "flow hook user-prompt-submit"
	)

	nativeHome := t.TempDir()
	t.Setenv("HOME", nativeHome)
	nh := claude.New().Hooks()
	if _, err := nh.InstallSessionStartHook(sessionCmd); err != nil {
		t.Fatalf("native SessionStart: %v", err)
	}
	if _, err := nh.InstallUserPromptSubmitHook(promptCmd); err != nil {
		t.Fatalf("native UserPromptSubmit: %v", err)
	}
	nativeSettings := readFile(t, filepath.Join(nativeHome, ".claude", "settings.json"))

	specHome := t.TempDir()
	t.Setenv("HOME", specHome)
	sh := loadClaudeSpec(t).Hooks()
	if _, err := sh.InstallSessionStartHook(sessionCmd); err != nil {
		t.Fatalf("spec SessionStart: %v", err)
	}
	if _, err := sh.InstallUserPromptSubmitHook(promptCmd); err != nil {
		t.Fatalf("spec UserPromptSubmit: %v", err)
	}
	specSettings := readFile(t, filepath.Join(specHome, ".claude", "settings.json"))

	if nativeSettings != specSettings {
		t.Errorf("settings.json differs\n--- native ---\n%s\n--- spec ---\n%s", nativeSettings, specSettings)
	}

	// Idempotency must match too: a second install changes nothing.
	added, err := sh.InstallSessionStartHook(sessionCmd)
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Error("spec adapter re-added an already-installed hook")
	}
	if got := readFile(t, filepath.Join(specHome, ".claude", "settings.json")); got != specSettings {
		t.Error("re-installing rewrote settings.json")
	}

	// And uninstall must return the file to its pre-install state.
	if _, err := sh.UninstallSessionStartHook(sessionCmd); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.UninstallUserPromptSubmitHook(promptCmd); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, filepath.Join(specHome, ".claude", "settings.json"))
	if strings.Contains(after, "flow hook") {
		t.Errorf("uninstall left flow's hooks behind:\n%s", after)
	}
}

func rel(t *testing.T, path, base string) string {
	t.Helper()
	r, err := filepath.Rel(base, path)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// assertTreesEqual compares two directory trees by relative path and
// content.
func assertTreesEqual(t *testing.T, a, b string) {
	t.Helper()
	collect := func(root string) map[string]string {
		out := map[string]string{}
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			r, _ := filepath.Rel(root, p)
			out[r] = string(data)
			return nil
		})
		return out
	}
	av, bv := collect(a), collect(b)
	if len(av) == 0 {
		t.Fatal("native install produced no files; the comparison would be vacuous")
	}
	for name, want := range av {
		got, ok := bv[name]
		if !ok {
			t.Errorf("spec install missing %s", name)
			continue
		}
		if got != want {
			t.Errorf("%s differs\n native: %q\n  spec:  %q", name, want, got)
		}
	}
	for name := range bv {
		if _, ok := av[name]; !ok {
			t.Errorf("spec install wrote an extra file: %s", name)
		}
	}
}
