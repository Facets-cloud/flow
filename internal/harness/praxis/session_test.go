package praxis

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const testSID = "019fb187-1e98-7ec5-aab5-d3b788b1fe55"

// tempHome points os.UserHomeDir at a temp dir and returns both it and
// the praxis sessions root beneath it.
func tempHome(t *testing.T) (home, sessionsDir string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	sessionsDir = filepath.Join(home, ".praxis", "agent", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, sessionsDir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidateSessionAcceptsCurrentLayout(t *testing.T) {
	_, sessions := tempHome(t)
	writeFile(t, filepath.Join(sessions, testSID, "session.jsonl"), "{}\n")

	if err := New().ValidateSession("/any/work/dir", testSID); err != nil {
		t.Errorf("ValidateSession = %v, want nil", err)
	}
}

// The legacy layout keyed the directory by cwd and prefixed the file with
// a start timestamp — it is NOT <sessions>/<id>.jsonl, which is what the
// old fallback looked for and which praxis never wrote.
func TestValidateSessionAcceptsLegacyCwdEncodedLayout(t *testing.T) {
	_, sessions := tempHome(t)
	writeFile(t, filepath.Join(sessions, "-Users-me-code-repo",
		"2026-06-27T16-19-12-066Z_"+testSID+".jsonl"), "{}\n")

	if err := New().ValidateSession("/Users/me/code/repo", testSID); err != nil {
		t.Errorf("ValidateSession = %v, want nil (legacy layout)", err)
	}
}

func TestValidateSessionMissingTranscript(t *testing.T) {
	_, sessions := tempHome(t)
	// A transcript for a DIFFERENT session must not satisfy the lookup.
	writeFile(t, filepath.Join(sessions, "0192ffff-0000-7000-8000-000000000000", "session.jsonl"), "{}\n")

	err := New().ValidateSession("/any", testSID)
	if err == nil {
		t.Fatal("ValidateSession = nil, want an error")
	}
	if !strings.Contains(err.Error(), testSID) {
		t.Errorf("error %q does not name the session id", err)
	}
}

// workDir is deliberately unused: praxis keys its store by session id, so
// a cwd mismatch is not a reason to refuse a bind.
func TestValidateSessionIgnoresWorkDir(t *testing.T) {
	_, sessions := tempHome(t)
	writeFile(t, filepath.Join(sessions, testSID, "session.jsonl"), "{}\n")

	for _, wd := range []string{"", "/somewhere/else", "/Users/me/code/repo"} {
		if err := New().ValidateSession(wd, testSID); err != nil {
			t.Errorf("ValidateSession(%q, …) = %v, want nil", wd, err)
		}
	}
}

func TestValidateSessionUsesStatFnSeam(t *testing.T) {
	tempHome(t)
	var probed []string
	orig := StatFn
	StatFn = func(path string) error {
		probed = append(probed, path)
		return nil // pretend the current-layout transcript exists
	}
	t.Cleanup(func() { StatFn = orig })

	if err := New().ValidateSession("/any", testSID); err != nil {
		t.Fatalf("ValidateSession = %v, want nil with a stubbed StatFn", err)
	}
	if len(probed) != 1 || !strings.HasSuffix(probed[0], filepath.Join(testSID, "session.jsonl")) {
		t.Errorf("StatFn probed %v, want one current-layout path", probed)
	}
}

func TestFindTranscriptPrefersCurrentLayout(t *testing.T) {
	home, sessions := tempHome(t)
	current := filepath.Join(sessions, testSID, "session.jsonl")
	writeFile(t, current, "{}\n")
	writeFile(t, filepath.Join(sessions, "-legacy-dir", "2026-01-01T00-00-00-000Z_"+testSID+".jsonl"), "{}\n")

	got, err := findTranscript(home, testSID)
	if err != nil {
		t.Fatalf("findTranscript: %v", err)
	}
	if got != current {
		t.Errorf("findTranscript = %q, want the current-layout path %q", got, current)
	}
}

// A resumed session can leave the same id under two cwd-encoded dirs; the
// live transcript is the one most recently written.
func TestFindTranscriptPrefersNewestLegacyMatch(t *testing.T) {
	home, sessions := tempHome(t)
	older := filepath.Join(sessions, "-repo-a", "2026-01-01T00-00-00-000Z_"+testSID+".jsonl")
	newer := filepath.Join(sessions, "-repo-b", "2026-06-01T00-00-00-000Z_"+testSID+".jsonl")
	writeFile(t, older, "{}\n")
	writeFile(t, newer, "{}\n")

	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(older, old, old); err != nil {
		t.Fatal(err)
	}

	got, err := findTranscript(home, testSID)
	if err != nil {
		t.Fatalf("findTranscript: %v", err)
	}
	if got != newer {
		t.Errorf("findTranscript = %q, want the most recently written %q", got, newer)
	}
}

// A legacy file whose name merely CONTAINS the id must not match — the
// glob requires the id to be the full suffix after the timestamp.
func TestFindTranscriptDoesNotMatchIDSubstring(t *testing.T) {
	home, sessions := tempHome(t)
	writeFile(t, filepath.Join(sessions, "-repo", "2026-01-01T00-00-00-000Z_"+testSID+"-backup.jsonl"), "{}\n")

	if got, err := findTranscript(home, testSID); err == nil {
		t.Errorf("findTranscript matched %q on a partial id", got)
	}
}

func TestRenderTranscriptEndToEnd(t *testing.T) {
	_, sessions := tempHome(t)
	transcript := strings.Join([]string{
		`{"type":"session","version":3,"id":"` + testSID + `"}`,
		`{"type":"message","message":{"role":"user","text":"ship it"}}`,
		`{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
	}, "\n")
	writeFile(t, filepath.Join(sessions, testSID, "session.jsonl"), transcript+"\n")

	var out strings.Builder
	if err := New().RenderTranscript("/any/cwd", testSID, false, &out); err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	want := "─── User ───\nship it\n\n─── Assistant ───\ndone\n"
	if got := out.String(); got != want {
		t.Errorf("RenderTranscript =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderTranscriptMissingSession(t *testing.T) {
	tempHome(t)
	var out strings.Builder
	err := New().RenderTranscript("/any/cwd", testSID, false, &out)
	if err == nil {
		t.Fatal("RenderTranscript = nil, want an error for a missing transcript")
	}
	if out.Len() != 0 {
		t.Errorf("wrote %q before failing, want nothing", out.String())
	}
}

func TestSkillPaths(t *testing.T) {
	home, _ := tempHome(t)

	skill, err := New().SkillInstallPath()
	if err != nil {
		t.Fatalf("SkillInstallPath: %v", err)
	}
	wantSkill := filepath.Join(home, ".praxis", "agent", "skills", "flow", "SKILL.md")
	if skill != wantSkill {
		t.Errorf("SkillInstallPath = %q, want %q", skill, wantSkill)
	}

	version, err := New().SkillVersionPath()
	if err != nil {
		t.Fatalf("SkillVersionPath: %v", err)
	}
	if want := filepath.Join(filepath.Dir(wantSkill), "VERSION"); version != want {
		t.Errorf("SkillVersionPath = %q, want %q", version, want)
	}
}

func TestInstallSkillWritesTreeAndUninstallRemovesIt(t *testing.T) {
	home, _ := tempHome(t)
	files := fstest.MapFS{
		"SKILL.md":              &fstest.MapFile{Data: []byte("# core")},
		"references/binding.md": &fstest.MapFile{Data: []byte("# binding")},
	}

	h := New()
	if err := h.InstallSkill(files); err != nil {
		t.Fatalf("InstallSkill: %v", err)
	}
	skillDir := filepath.Join(home, ".praxis", "agent", "skills", "flow")
	for rel, want := range map[string]string{
		"SKILL.md":              "# core",
		"references/binding.md": "# binding",
	} {
		got, err := os.ReadFile(filepath.Join(skillDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}

	if err := h.UninstallSkill(); err != nil {
		t.Fatalf("UninstallSkill: %v", err)
	}
	if _, err := os.Stat(skillDir); !os.IsNotExist(err) {
		t.Errorf("skill dir survived uninstall (stat err=%v)", err)
	}

	// Uninstalling again is a no-op, not an error: `flow skill uninstall`
	// now sweeps every harness and must not fail on the ones it already
	// removed, or on a harness that was never installed.
	if err := h.UninstallSkill(); err != nil {
		t.Errorf("second UninstallSkill = %v, want nil", err)
	}
}

// A failed install must surface the reason rather than reporting success
// with a partially written skill tree.
func TestInstallSkillReportsWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission bits are not enforced")
	}
	home, _ := tempHome(t)

	skillDir := filepath.Join(home, ".praxis", "agent", "skills", "flow")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(skillDir, 0o500); err != nil { // r-x: no writes
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(skillDir, 0o755) })

	files := fstest.MapFS{"SKILL.md": &fstest.MapFile{Data: []byte("# core")}}
	err := New().InstallSkill(files)
	if err == nil {
		t.Fatal("InstallSkill = nil, want a write error")
	}
	if !strings.Contains(err.Error(), "SKILL.md") {
		t.Errorf("error %q does not name the file it failed on", err)
	}
}
