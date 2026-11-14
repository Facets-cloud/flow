package prx

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"flow/internal/harness"
)

const testID = "0b7e3f0a-1c2d-4e5f-8a9b-0c1d2e3f4a5b"

// isolate points prx's agent and session dirs at a temp tree.
func isolate(t *testing.T) (agent, sessions string) {
	t.Helper()
	agent = t.TempDir()
	sessions = filepath.Join(agent, "sessions")
	t.Setenv("PRAXIS_CODING_AGENT_DIR", agent)
	t.Setenv("PRAXIS_CODING_AGENT_SESSION_DIR", "")
	return agent, sessions
}

func TestCommands(t *testing.T) {
	h := New()
	if got, want := h.LaunchCmd(testID, "work", harness.LaunchOpts{}), "prx -session-id "+testID+" -prompt 'work'"; got != want {
		t.Errorf("LaunchCmd=%q, want %q", got, want)
	}
	if got := h.LaunchCmd(testID, "work", harness.LaunchOpts{SkipPermissions: true}); !strings.Contains(got, " -permission-mode yolo ") {
		t.Errorf("LaunchCmd skip=%q", got)
	}
	if got, want := h.ResumeCmd(testID, harness.LaunchOpts{}), "prx -session-id "+testID+" -resume "+testID; got != want {
		t.Errorf("ResumeCmd=%q, want %q", got, want)
	}
	if got := h.ResumeCmd(testID, harness.LaunchOpts{Inject: "follow up"}); !strings.Contains(got, harness.InjectionMarker+"\nfollow up") {
		t.Errorf("ResumeCmd missing injection: %q", got)
	}
	argv := h.AutoRunArgv(testID, "work", harness.LaunchOpts{SkipPermissions: true, Inject: "x", LastMessagePath: "/tmp/ignored"})
	want := []string{"prx", "run", "-session", testID, "-max-turns", "5000", "-permission-mode", "yolo",
		"-prompt", "work\n\n" + harness.InjectionMarker + "\nx"}
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("AutoRunArgv=%q\nwant      %q", argv, want)
	}
}

func TestSessionIDs(t *testing.T) {
	h := New()
	id, err := h.NewSessionID("")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ValidateSessionID(id); err != nil {
		t.Errorf("minted id rejected: %v", err)
	}
	// prx's own ids are UUIDv7; --here binds those.
	if err := h.ValidateSessionID("019f8f30-194b-7c83-8564-bff77c5e0147"); err != nil {
		t.Errorf("UUIDv7 rejected: %v", err)
	}
	for _, bad := range []string{"", "../x", strings.ToUpper(testID)} {
		if h.ValidateSessionID(bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func writeTranscript(t *testing.T, sessions, id, body string, gz bool) {
	t.Helper()
	dir := filepath.Join(sessions, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path, data := filepath.Join(dir, "session.jsonl"), []byte(body)
	if gz {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		zw.Write(data)
		zw.Close()
		path, data = path+".gz", b.Bytes()
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

const sampleTranscript = `{"type":"session","version":3,"id":"x","cwd":"/w"}
{"type":"model_change","id":"a","parentId":null}
{"type":"message","id":"b","message":{"role":"user","text":"hello"}}
{"type":"message","id":"c","message":{"role":"assistant","content":[{"type":"thinking","text":"hmm"},{"type":"text","text":"hi"},{"type":"toolCall","id":"t1","name":"bash","arguments":{"command":"ls"}}]}}
{"type":"message","id":"d","message":{"role":"toolResult","toolCallId":"t1","toolName":"bash","isError":true,"content":[{"type":"text","text":"boom"}]}}
`

func TestRenderTranscript(t *testing.T) {
	_, sessions := isolate(t)
	writeTranscript(t, sessions, testID, sampleTranscript, false)

	var full bytes.Buffer
	if err := New().RenderTranscript("ignored", testID, false, &full); err != nil {
		t.Fatal(err)
	}
	want := "─── User ───\nhello\n\n─── Thinking ───\nhmm\n\n─── Assistant ───\nhi\n\n" +
		"─── Tool: bash ───\n{\"command\":\"ls\"}\n\n─── Result (error) ───\nboom\n"
	if full.String() != want {
		t.Errorf("full render:\n%s\nwant:\n%s", full.String(), want)
	}

	var compact bytes.Buffer
	if err := New().RenderTranscript("", testID, true, &compact); err != nil {
		t.Fatal(err)
	}
	if s := compact.String(); strings.Contains(s, "Thinking") || strings.Contains(s, "Result") || !strings.Contains(s, "hi") {
		t.Errorf("compact render:\n%s", s)
	}
}

func TestTranscriptGzipAndValidateSession(t *testing.T) {
	_, sessions := isolate(t)
	h := New()
	if h.ValidateSession("/any", testID) == nil {
		t.Error("ValidateSession accepted a missing session")
	}
	writeTranscript(t, sessions, testID, sampleTranscript, true)
	if err := h.ValidateSession("/any", testID); err != nil {
		t.Errorf("ValidateSession on .gz: %v", err)
	}
	var out bytes.Buffer
	if err := h.RenderTranscript("", testID, true, &out); err != nil || !strings.Contains(out.String(), "hello") {
		t.Errorf("gz render err=%v out=%q", err, out.String())
	}
}

func TestSessionDirOverride(t *testing.T) {
	isolate(t)
	custom := t.TempDir()
	t.Setenv("PRAXIS_CODING_AGENT_SESSION_DIR", custom)
	writeTranscript(t, custom, testID, sampleTranscript, false)
	if err := New().ValidateSession("", testID); err != nil {
		t.Errorf("session dir override ignored: %v", err)
	}
}

func TestLiveSessionIDs(t *testing.T) {
	_, sessions := isolate(t)
	origAlive, origHost := PIDAlive, Hostname
	t.Cleanup(func() { PIDAlive, Hostname = origAlive, origHost })
	PIDAlive = func(pid int) bool { return pid == 100 }
	Hostname = func() (string, error) { return "here", nil }

	ownerDir := filepath.Join(sessions, ".owner")
	if err := os.MkdirAll(ownerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(id string, pid int, host string) {
		b, _ := json.Marshal(map[string]any{"sessionId": id, "pid": pid, "host": host})
		if err := os.WriteFile(filepath.Join(ownerDir, id+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("aaaa", 100, "here")  // alive here
	write("bbbb", 200, "here")  // dead here
	write("CCCC", 300, "other") // another machine: assume live
	os.WriteFile(filepath.Join(ownerDir, "junk.json"), []byte("{"), 0o644)

	live, err := New().LiveSessionIDs()
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{"aaaa": 1, "cccc": 1}; !reflect.DeepEqual(live, want) {
		t.Errorf("live=%v, want %v", live, want)
	}
}

func TestLiveSessionIDsNoOwnerDir(t *testing.T) {
	isolate(t)
	live, err := New().LiveSessionIDs()
	if err != nil || len(live) != 0 {
		t.Errorf("live=%v err=%v, want empty", live, err)
	}
}

func TestHooksPreserveSettings(t *testing.T) {
	agent, _ := isolate(t)
	path := filepath.Join(agent, "settings.json")
	if err := os.WriteFile(path, []byte(`{"model":"keep-me"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h := New()
	if added, err := h.InstallSessionStartHook("flow hook session-start"); err != nil || !added {
		t.Fatalf("install added=%v err=%v", added, err)
	}
	if added, _ := h.InstallSessionStartHook("flow hook session-start"); added {
		t.Error("second install reported a change")
	}
	for _, install := range []func(string) (bool, error){h.InstallUserPromptSubmitHook, h.InstallPreToolUseHook, h.InstallPostToolUseHook, h.InstallStopHook} {
		if added, err := install("flow hook x"); err != nil || !added {
			t.Fatalf("install added=%v err=%v", added, err)
		}
	}

	var s map[string]any
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if s["model"] != "keep-me" {
		t.Errorf("other settings lost: %s", raw)
	}
	hooks := s["hooks"].(map[string]any)
	ss := hooks["SessionStart"].([]any)[0].(map[string]any)
	if ss["matcher"] != "startup|resume|compact" {
		t.Errorf("SessionStart matcher=%v", ss["matcher"])
	}
	for _, ev := range []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
		entry := hooks[ev].([]any)[0].(map[string]any)
		if _, has := entry["matcher"]; has {
			t.Errorf("%s has a matcher; prx would filter on it: %v", ev, entry)
		}
	}

	if removed, err := h.UninstallSessionStartHook("flow hook session-start"); err != nil || !removed {
		t.Errorf("uninstall removed=%v err=%v", removed, err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "SessionStart") {
		t.Errorf("SessionStart left behind: %s", raw)
	}
}

func TestSkillPaths(t *testing.T) {
	agent, _ := isolate(t)
	h := New()
	p, err := h.SkillInstallPath()
	if err != nil || p != filepath.Join(agent, "skills", "flow", "SKILL.md") {
		t.Errorf("SkillInstallPath=%q err=%v", p, err)
	}
	v, _ := h.SkillVersionPath()
	if v != filepath.Join(agent, "skills", "flow", "VERSION") {
		t.Errorf("SkillVersionPath=%q", v)
	}
}
