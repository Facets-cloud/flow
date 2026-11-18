package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Scan reads claude + prx transcripts from disk, drops noise, flow's own
// headless sessions and bound sessions, and groups by cwd.
func TestOnboardScanAndDigest(t *testing.T) {
	setupFlowRoot(t)
	home := os.Getenv("HOME")
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("PRAXIS_CODING_AGENT_SESSION_DIR", filepath.Join(home, "prx-sessions"))
	for _, v := range []string{"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "PRAXIS_SESSION_ID"} {
		t.Setenv(v, "")
	}
	write := func(p, body string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	proj := filepath.Join(home, ".claude", "projects", "-repo")
	write(filepath.Join(proj, "c1.jsonl"),
		`{"type":"user","cwd":"/repo","message":{"role":"user","content":"<system-reminder>noise</system-reminder>"}}`+"\n"+
			`{"type":"user","cwd":"/repo","message":{"role":"user","content":"add oauth login"}}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done, PR opened"}]}}`+"\n")
	write(filepath.Join(proj, "sweep.jsonl"),
		`{"type":"user","cwd":"/repo","message":{"role":"user","content":"You are running an automated close-out sweep for x"}}`+"\n")
	write(filepath.Join(home, "prx-sessions", "p1", "session.jsonl"),
		`{"type":"session","id":"p1","cwd":"/other"}`+"\n"+
			`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"fix the flaky test"}]}}`+"\n")
	write(filepath.Join(home, "prx-sessions", "old", "session.jsonl"),
		`{"type":"session","id":"old","cwd":"/old"}`+"\n"+
			`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"ancient work"}]}}`+"\n")
	// Regression: a session whose cwd IS the temp root, and a codex thread
	// that is only flow's probe, are not candidates; real codex work is.
	write(filepath.Join(proj, "tmproot.jsonl"),
		`{"type":"user","cwd":"/private/tmp","message":{"role":"user","content":"scratch"}}`+"\n")
	cx := filepath.Join(home, ".codex", "sessions", "2026", "11", "18")
	codexUser := func(t string) string {
		return `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + t + `"}]}}` + "\n"
	}
	write(filepath.Join(cx, "rollout-2026-11-18T10-00-00-11111111-1111-4111-8111-111111111111.jsonl"),
		`{"type":"session_meta","payload":{"cwd":"/cx"}}`+"\n"+codexUser("Reply exactly with OK. Do not make any changes."))
	write(filepath.Join(cx, "rollout-2026-11-18T11-00-00-22222222-2222-4222-8222-222222222222.jsonl"),
		`{"type":"session_meta","payload":{"cwd":"/cx"}}`+"\n"+codexUser("migrate the db to postgres 16"))
	past := time.Now().AddDate(0, 0, -30)
	os.Chtimes(filepath.Join(home, "prx-sessions", "old", "session.jsonl"), past, past)

	out := captureStdout(t, func() {
		if rc := cmdOnboard([]string{"scan"}); rc != 0 {
			t.Fatalf("scan rc=%d", rc)
		}
	})
	for _, want := range []string{"3 unbound sessions in 3 dirs", "/repo  [claude×1]  last", "(dir no longer exists)", "add oauth login  (claude:c1)", "fix the flaky test  (prx:p1)",
		"migrate the db to postgres 16  (codex:22222222-2222-4222-8222-222222222222)"} {
		if !strings.Contains(out, want) {
			t.Errorf("scan missing %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"system-reminder", "close-out sweep", "ancient work", "/private/tmp", "Reply exactly"} {
		if strings.Contains(out, bad) {
			t.Errorf("scan leaked %q:\n%s", bad, out)
		}
	}

	out = captureStdout(t, func() {
		if rc := cmdOnboard([]string{"digest", "claude:c1"}); rc != 0 {
			t.Fatalf("digest rc=%d", rc)
		}
	})
	if !strings.Contains(out, "- add oauth login") || !strings.Contains(out, "done, PR opened") {
		t.Errorf("digest:\n%s", out)
	}
}

// Over the cap the digest keeps the opening and newest prompts.
func TestWriteDigestCapsMiddle(t *testing.T) {
	s := &sessionSummary{onboardSession: onboardSession{harness: "claude", id: "x"}}
	for i := 0; i < 50; i++ {
		s.prompts = append(s.prompts, strings.Repeat("p", 90)+string(rune('A'+i%26)))
	}
	var b bytes.Buffer
	writeDigest(&b, s, 1000)
	out := b.String()
	if !strings.Contains(out, "prompts omitted") || !strings.HasPrefix(strings.Split(out, "- ")[1], s.prompts[0]) {
		t.Errorf("digest:\n%s", out)
	}
	if !strings.Contains(out, s.prompts[49]) || len(out) > 1400 {
		t.Errorf("digest missing newest prompt or over cap (%d):\n%s", len(out), out)
	}
}
