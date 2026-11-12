package codex

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/harness"
)

const testThreadID = "019ff4b1-6162-7263-974f-f5f1866ad0fe"

func TestNewSessionIDParsesThreadStarted(t *testing.T) {
	orig := ProbeRunner
	t.Cleanup(func() { ProbeRunner = orig })
	ProbeRunner = func(string) ([]byte, error) {
		return []byte("noise\n{\"type\":\"thread.started\",\"thread_id\":\"" + testThreadID + "\"}\n"), nil
	}
	got, err := New().NewSessionID("")
	if err != nil {
		t.Fatal(err)
	}
	if got != testThreadID {
		t.Errorf("NewSessionID=%q, want %q", got, testThreadID)
	}
}

func TestNewSessionIDRejectsMissingOrInvalidThread(t *testing.T) {
	orig := ProbeRunner
	t.Cleanup(func() { ProbeRunner = orig })
	ProbeRunner = func(string) ([]byte, error) { return []byte(`{"type":"thread.started","thread_id":"nope"}`), nil }
	if _, err := New().NewSessionID(""); err == nil {
		t.Error("NewSessionID accepted invalid thread id")
	}
	ProbeRunner = func(string) ([]byte, error) { return []byte(`{"type":"turn.started"}`), nil }
	if _, err := New().NewSessionID(""); err == nil {
		t.Error("NewSessionID accepted output without thread.started")
	}
	ProbeRunner = func(string) ([]byte, error) { return nil, errors.New("not installed") }
	if _, err := New().NewSessionID(""); err == nil {
		t.Error("NewSessionID swallowed probe error")
	}
}

func TestCommands(t *testing.T) {
	h := New()
	if got, want := h.LaunchCmd(testThreadID, "work", harness.LaunchOpts{}), "codex resume -s workspace-write -a on-request "+testThreadID+" 'work'"; got != want {
		t.Errorf("LaunchCmd=%q, want %q", got, want)
	}
	if got := h.LaunchCmd(testThreadID, "work", harness.LaunchOpts{SkipPermissions: true}); !strings.HasPrefix(got, "codex --dangerously-bypass-approvals-and-sandbox resume ") {
		t.Errorf("LaunchCmd skip=%q", got)
	}
	if got := h.ResumeCmd(testThreadID, harness.LaunchOpts{Inject: "follow up"}); !strings.Contains(got, harness.InjectionMarker+"\nfollow up") {
		t.Errorf("ResumeCmd missing injection: %q", got)
	}
	argv := h.AutoRunArgv(testThreadID, "work", harness.LaunchOpts{SkipPermissions: true})
	want := []string{"codex", "exec", "resume", "--dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-hook-trust", testThreadID, "work"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("AutoRunArgv=%q, want %q", argv, want)
	}
}

// F1: the probe mints the thread read-only/approval=never and `codex
// resume` keeps a thread's first-turn settings unless told otherwise, so a
// sandboxed interactive session must pass its sandbox + approval policy
// explicitly — and make the flow root writable, or `flow show task` dies
// with "attempt to write a readonly database".
func TestInteractiveCommandsAreWorkspaceWriteOnRequest(t *testing.T) {
	h := New()
	opts := harness.LaunchOpts{WritableDirs: []string{"/tmp/flow root"}}
	wantPrefix := "codex resume -s workspace-write -a on-request --add-dir '/tmp/flow root' " + testThreadID
	if got := h.LaunchCmd(testThreadID, "work", opts); got != wantPrefix+" 'work'" {
		t.Errorf("LaunchCmd=%q, want %q", got, wantPrefix+" 'work'")
	}
	if got := h.ResumeCmd(testThreadID, opts); got != wantPrefix {
		t.Errorf("ResumeCmd=%q, want %q", got, wantPrefix)
	}
	// Skip-permissions is unchanged: bypass, no sandbox/approval/add-dir.
	skip := harness.LaunchOpts{SkipPermissions: true, WritableDirs: []string{"/tmp/x"}}
	for _, got := range []string{h.LaunchCmd(testThreadID, "work", skip), h.ResumeCmd(testThreadID, skip)} {
		if !strings.HasPrefix(got, "codex --dangerously-bypass-approvals-and-sandbox resume "+testThreadID) {
			t.Errorf("skip-permissions command changed: %q", got)
		}
		if strings.Contains(got, "--add-dir") || strings.Contains(got, " -s ") {
			t.Errorf("skip-permissions command carries sandbox flags: %q", got)
		}
	}
	// The live-session guard still sees the sandboxed interactive argv.
	orig := PSRunner
	t.Cleanup(func() { PSRunner = orig })
	PSRunner = func() ([]byte, error) {
		return []byte("300 codex resume -s workspace-write -a on-request --add-dir /tmp/flowroot " + testThreadID + " work"), nil
	}
	live, err := h.LiveSessionIDs()
	if err != nil {
		t.Fatal(err)
	}
	if live[testThreadID] != 1 {
		t.Errorf("live=%v, want sandboxed interactive codex detected", live)
	}
}

func TestLiveSessionIDs(t *testing.T) {
	orig := PSRunner
	t.Cleanup(func() { PSRunner = orig })
	PSRunner = func() ([]byte, error) {
		return []byte("100 codex resume " + testThreadID + "\n101 codex exec resume " + testThreadID + "\n102 grep resume " + testThreadID), nil
	}
	live, err := New().LiveSessionIDs()
	if err != nil {
		t.Fatal(err)
	}
	if live[testThreadID] != 2 {
		t.Errorf("live=%v, want two Codex processes", live)
	}
}

// An autonomous run's argv (AutoRunArgv) puts the bypass flag BETWEEN
// `resume` and the thread id; the live-session probe must still see it,
// or `flow do` opens a second writer on a thread an auto run is driving.
func TestLiveSessionIDsSeesAutoRunArgv(t *testing.T) {
	orig := PSRunner
	t.Cleanup(func() { PSRunner = orig })
	argv := New().AutoRunArgv(testThreadID, "work", harness.LaunchOpts{SkipPermissions: true})
	PSRunner = func() ([]byte, error) {
		return []byte("200 " + strings.Join(argv, " ")), nil
	}
	live, err := New().LiveSessionIDs()
	if err != nil {
		t.Fatal(err)
	}
	if live[testThreadID] != 1 {
		t.Errorf("live=%v, want the auto-run codex process (%q) detected", live, strings.Join(argv, " "))
	}
}

// ps prints argv unquoted, so a flow root with a space turns the
// `--add-dir` value into several tokens. The guard must still find the
// thread id, or `flow do` starts a second `codex resume` on a live thread.
func TestLiveSessionIDsSeesAddDirWithWhitespace(t *testing.T) {
	orig := PSRunner
	t.Cleanup(func() { PSRunner = orig })
	other := "22222222-2222-4222-8222-222222222222"
	PSRunner = func() ([]byte, error) {
		return []byte(strings.Join([]string{
			"300 codex resume -s workspace-write -a on-request --add-dir /tmp/flow root " + testThreadID,
			"301 codex resume -s workspace-write -a on-request --add-dir /tmp/my flow root --add-dir /x y " + testThreadID + " keep going on " + other,
			// Prose after a non-resume codex command is not a session.
			"302 codex exec please resume work on " + other,
		}, "\n")), nil
	}
	live, err := New().LiveSessionIDs()
	if err != nil {
		t.Fatal(err)
	}
	if live[testThreadID] != 2 {
		t.Errorf("live=%v, want both whitespace-path codex processes detected", live)
	}
	if live[other] != 0 {
		t.Errorf("live=%v, an id in the prompt text must not count as a live session", live)
	}
}

func TestRenderJSONL(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"` + testThreadID + `"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"hidden"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call","name":"functions.exec","input":"ls"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call_output","output":"files"}}`,
	}, "\n")
	var out bytes.Buffer
	if err := RenderJSONL(strings.NewReader(input), false, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"─── User ───\nhello", "─── Assistant ───\nhi", "─── Tool: functions.exec ───\nls", "─── Result ───\nfiles"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "hidden") {
		t.Errorf("rendered developer instruction: %s", got)
	}
	out.Reset()
	if err := RenderJSONL(strings.NewReader(input), true, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "files") {
		t.Errorf("compact render included tool result: %s", out.String())
	}
}

func TestSkillAndSessionStartHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	h := New()
	files := os.DirFS(t.TempDir())
	if err := h.InstallSkill(files); err != nil {
		t.Fatalf("InstallSkill empty fs: %v", err)
	}
	// An empty filesystem still establishes the destination path through
	// the harness's path methods, while hooks must be written separately.
	if added, err := h.InstallSessionStartHook("flow hook session-start"); err != nil || !added {
		t.Fatalf("InstallSessionStartHook added=%v err=%v", added, err)
	}
	if added, err := h.InstallSessionStartHook("flow hook session-start"); err != nil || added {
		t.Fatalf("idempotent install added=%v err=%v", added, err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "startup|resume") || !strings.Contains(string(raw), "flow hook session-start") {
		t.Errorf("unexpected hooks file: %s", raw)
	}
	if removed, err := h.UninstallSessionStartHook("flow hook session-start"); err != nil || !removed {
		t.Fatalf("UninstallSessionStartHook removed=%v err=%v", removed, err)
	}
}

// Codex keeps sessions under $CODEX_HOME when set (default ~/.codex);
// flow transcript must look there, not hard-code ~/.codex.
func TestRenderTranscriptHonorsCodexHome(t *testing.T) {
	home := t.TempDir()
	codexHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	dir := filepath.Join(codexHome, "sessions", "2026", "11", "06")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"from codex home"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-11-06T00-00-00-"+testThreadID+".jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	if err := New().(*codex).RenderTranscript("", testThreadID, false, &buf); err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	if !strings.Contains(buf.String(), "from codex home") {
		t.Errorf("output %q missing transcript text", buf.String())
	}
}

// The probe mints the thread with `codex exec`, which records its cwd in
// the rollout; it must run in the task's work_dir, not the caller's cwd.
func TestNewSessionIDProbesInWorkDir(t *testing.T) {
	orig := ProbeRunner
	t.Cleanup(func() { ProbeRunner = orig })
	var gotDir string
	ProbeRunner = func(workDir string) ([]byte, error) {
		gotDir = workDir
		return []byte(`{"type":"thread.started","thread_id":"` + testThreadID + `"}`), nil
	}
	if _, err := New().NewSessionID("/tmp/the-task-dir"); err != nil {
		t.Fatal(err)
	}
	if gotDir != "/tmp/the-task-dir" {
		t.Errorf("probe workDir = %q, want the task's work_dir", gotDir)
	}
}

// LastMessagePath maps to codex exec's -o/--output-last-message, placed
// before the thread id so the live-session regex still matches.
func TestAutoRunArgvLastMessagePath(t *testing.T) {
	h := New()
	argv := h.AutoRunArgv(testThreadID, "work", harness.LaunchOpts{SkipPermissions: true, LastMessagePath: "/runs/x.last.txt"})
	want := []string{"codex", "exec", "resume", "--dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-hook-trust", "-o", "/runs/x.last.txt", testThreadID, "work"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("AutoRunArgv=%q, want %q", argv, want)
	}
	orig := PSRunner
	t.Cleanup(func() { PSRunner = orig })
	PSRunner = func() ([]byte, error) { return []byte("300 " + strings.Join(argv, " ")), nil }
	live, err := h.LiveSessionIDs()
	if err != nil {
		t.Fatal(err)
	}
	if live[testThreadID] != 1 {
		t.Errorf("live=%v, want the -o auto run detected", live)
	}
}
