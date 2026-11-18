package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

// Review regression: three 300-byte prompts under a 500-byte cap must stay
// within the cap and still include the newest prompt.
func TestWriteDigestNeverExceedsCap(t *testing.T) {
	s := &sessionSummary{onboardSession: onboardSession{harness: "claude", id: "x"},
		prompts: []string{strings.Repeat("a", 300), strings.Repeat("é", 150), strings.Repeat("c", 299) + "Z"}}
	var b bytes.Buffer
	writeDigest(&b, s, 500)
	got := 0
	for _, l := range strings.Split(b.String(), "\n") {
		if strings.HasPrefix(l, "- ") && !strings.HasPrefix(l, "- … ") {
			got += len(l) - 2
			if !utf8.ValidString(l) {
				t.Errorf("clipped mid-rune: %q", l)
			}
		}
	}
	if got > 500 || !strings.Contains(b.String(), s.prompts[2]) {
		t.Errorf("prompt bytes %d > 500 or newest missing:\n%s", got, b.String())
	}
}

// Cap leaving 1–2 bytes: no ellipsis overshoot, no blank "- " lines.
func TestWriteDigestTinyRemainder(t *testing.T) {
	for cap := 1; cap <= 12; cap++ {
		s := &sessionSummary{onboardSession: onboardSession{harness: "claude", id: "x"},
			prompts: []string{"aaaaaaaaaa", "bbbbbbbbbb", "ccccccccc"}}
		var b bytes.Buffer
		writeDigest(&b, s, cap)
		got := 0
		for _, l := range strings.Split(b.String(), "\n") {
			if l == "- " {
				t.Errorf("cap %d: blank prompt line:\n%s", cap, b.String())
			}
			if strings.HasPrefix(l, "- ") && !strings.HasPrefix(l, "- … ") {
				got += len(l) - 2
			}
		}
		if got > cap {
			t.Errorf("cap %d: %d prompt bytes:\n%s", cap, got, b.String())
		}
	}
}

func TestUserPromptKeepsMarkupPrompts(t *testing.T) {
	if userPrompt("<div>Login</div> renders wrong; fix the layout") == "" {
		t.Error("a real prompt starting with markup was dropped")
	}
	if userPrompt("<system-reminder>x</system-reminder>") != "" {
		t.Error("injected system-reminder kept")
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
