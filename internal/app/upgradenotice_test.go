package app

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/harness/codex"
)

// setVersion pins the running binary's Version for one test.
func setVersion(t *testing.T, v string) {
	t.Helper()
	old := Version
	Version = v
	t.Cleanup(func() { Version = old })
}

func payloadFor(sid string) string { return `{"session_id":"` + sid + `"}` }

func hookStart(t *testing.T, sid string) string {
	t.Helper()
	var out string
	withHookStdin(t, payloadFor(sid), func() {
		out = busHookContext(t, captureStdout(t, func() { cmdHookSessionStart(nil) }))
	})
	return out
}

func hookUPS(t *testing.T, sid string) string {
	t.Helper()
	var out string
	withHookStdin(t, payloadFor(sid), func() {
		out = busHookContext(t, captureStdout(t, func() { cmdHookUserPromptSubmit(nil) }))
	})
	return out
}

func hookTool(t *testing.T, sid string) string {
	t.Helper()
	var out string
	withHookStdin(t, payloadFor(sid), func() {
		out = busHookContext(t, captureStdout(t, func() { cmdHookPreToolUse(nil) }))
	})
	return out
}

func clearSessionEnv(t *testing.T) {
	t.Helper()
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}
	t.Cleanup(func() { hookSessionID = "" })
}

const upgradeMarker = "[flow-upgrade]"

func TestUpgradeNoticeOncePerVersionChange(t *testing.T) {
	setupFlowRoot(t)
	clearSessionEnv(t)
	const sid = "11111111-2222-4333-8444-555555555555"
	mkBusTask(t, openFlowDB(t), "up-task", sid)

	setVersion(t, "v0.1.0-alpha.40")
	if out := hookStart(t, sid); strings.Contains(out, upgradeMarker) {
		t.Fatalf("SessionStart must record silently; got:\n%s", out)
	}
	if out := hookUPS(t, sid); strings.Contains(out, upgradeMarker) {
		t.Fatalf("no notice while the version is unchanged; got:\n%s", out)
	}

	setVersion(t, "v0.1.0-alpha.41") // binary swapped under the session
	out := hookUPS(t, sid)
	for _, want := range []string{
		upgradeMarker,
		"v0.1.0-alpha.40 → v0.1.0-alpha.41",
		"re-load the flow skill",
		filepath.Join(".claude", "skills", "flow", "references", "upgrade-notes.md"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("notice missing %q; got:\n%s", want, out)
		}
	}
	if !strings.Contains(out, `flow session → task`) {
		t.Errorf("the prompt anchor must still be emitted alongside the notice; got:\n%s", out)
	}
	for i := 0; i < 2; i++ {
		if out := hookUPS(t, sid); strings.Contains(out, upgradeMarker) {
			t.Fatalf("notice repeated on a later prompt:\n%s", out)
		}
		if out := hookTool(t, sid); strings.Contains(out, upgradeMarker) {
			t.Fatalf("notice repeated on a later tool call:\n%s", out)
		}
	}

	// The next change notifies again — every version change, once.
	setVersion(t, "v0.1.0-alpha.42")
	if out := hookTool(t, sid); !strings.Contains(out, "v0.1.0-alpha.41 → v0.1.0-alpha.42") {
		t.Errorf("second upgrade should notify via PreToolUse; got:\n%s", out)
	}
}

// A session that started before this feature shipped has no row: its
// first hook on the new binary tells it once, old version unknown.
func TestUpgradeNoticeNoRowSessionNotifiedOnce(t *testing.T) {
	setupFlowRoot(t)
	clearSessionEnv(t)
	setVersion(t, "v0.1.0-alpha.41")
	const sid = "aaaaaaaa-2222-4333-8444-555555555555" // unbound: no task
	out := hookTool(t, sid)
	if !strings.Contains(out, upgradeMarker) || !strings.Contains(out, "an earlier version → v0.1.0-alpha.41") {
		t.Fatalf("no-row session should be told once; got:\n%s", out)
	}
	if out := hookUPS(t, sid); out != "" {
		t.Fatalf("unbound prompt after the notice must be silent; got:\n%s", out)
	}
}

func TestUpgradeNoticeSessionStartResetsWithoutNotice(t *testing.T) {
	setupFlowRoot(t)
	clearSessionEnv(t)
	const sid = "bbbbbbbb-2222-4333-8444-555555555555"
	setVersion(t, "v0.1.0-alpha.40")
	hookStart(t, sid)
	setVersion(t, "v0.1.0-alpha.41")
	// A resume (or compaction) on the new binary reloads the skill itself.
	if out := hookStart(t, sid); strings.Contains(out, upgradeMarker) {
		t.Fatalf("SessionStart must not notify; got:\n%s", out)
	}
	if out := hookUPS(t, sid); strings.Contains(out, upgradeMarker) {
		t.Fatalf("already current after SessionStart; got:\n%s", out)
	}
}

// A dev build has no version identity to compare: no record, no notice.
func TestUpgradeNoticeDevBuildSilent(t *testing.T) {
	setupFlowRoot(t)
	clearSessionEnv(t)
	setVersion(t, "dev")
	const sid = "cccccccc-2222-4333-8444-555555555555"
	if out := hookTool(t, sid); out != "" {
		t.Fatalf("dev build must stay silent; got:\n%s", out)
	}
}

func TestUpgradeNoticeCodexPointsAtCodexSkill(t *testing.T) {
	setupFlowRoot(t)
	clearSessionEnv(t)
	const sid = "019ff4b1-6162-7263-974f-f5f1866ad0aa"
	db := openFlowDB(t)
	mkBusTask(t, db, "codex-up", sid)
	if _, err := db.Exec(`UPDATE tasks SET harness='codex' WHERE slug='codex-up'`); err != nil {
		t.Fatal(err)
	}
	setVersion(t, "v0.1.0-alpha.40")
	hookStart(t, sid)
	setVersion(t, "v0.1.0-alpha.41")
	out := hookUPS(t, sid)
	p, err := codex.New().SkillInstallPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(p), "references", "upgrade-notes.md")
	if !strings.Contains(out, want) {
		t.Errorf("Codex notice should name %s; got:\n%s", want, out)
	}
	if strings.Contains(out, filepath.Join(".claude", "skills")) {
		t.Errorf("Codex notice must not point at the Claude skill; got:\n%s", out)
	}
}

// The notes ship inside the skill (so the notice can point at an
// installed file) and start with the harness-wake entry.
func TestUpgradeNotesShipInSkill(t *testing.T) {
	b, err := fs.ReadFile(skillFiles(), "references/upgrade-notes.md")
	if err != nil {
		t.Fatalf("references/upgrade-notes.md must be embedded in the skill: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, "\n## ") {
		t.Error("upgrade notes need one `## <version>` section per release")
	}
	for _, want := range []string{"TaskStop", "Monitor", "bare"} {
		if !strings.Contains(s, want) {
			t.Errorf("first entry (harness wake) should mention %q", want)
		}
	}
}
