package kitty

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestSpawnTabBasicNoEnv verifies the two-call argv sequence with no
// env vars:
//  1. RunnerOutput: kitty @ launch --type=tab --tab-title=<t> --cwd=<cwd>  (returns window id)
//  2. Runner:       kitty @ send-text --match=id:<id> " <command>\n"      (leading space for histignorespace)
func TestSpawnTabBasicNoEnv(t *testing.T) {
	var launchArgs []string
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		launchArgs = append([]string(nil), args...)
		return []byte("42\n"), nil
	})

	var sendArgs []string
	old := Runner
	Runner = func(args []string) error {
		sendArgs = append([]string(nil), args...)
		return nil
	}
	t.Cleanup(func() { Runner = old })

	if err := SpawnTab("my-task", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}

	wantLaunch := []string{"@", "launch", "--type=tab", "--tab-title=my-task", "--cwd=/tmp"}
	if !slices.Equal(launchArgs, wantLaunch) {
		t.Errorf("launch argv = %v; want %v", launchArgs, wantLaunch)
	}
	wantSend := []string{"@", "send-text", "--match=id:42", " echo hi\n"}
	if !slices.Equal(sendArgs, wantSend) {
		t.Errorf("send argv = %v; want %v", sendArgs, wantSend)
	}
}

// TestSpawnTabEnvVarsSorted verifies env vars are emitted alphabetically,
// each value shell-quoted, all space-separated, before the command. This
// matches the iterm/terminal/zellij env-prefix contract exactly.
func TestSpawnTabEnvVarsSorted(t *testing.T) {
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return []byte("17\n"), nil
	})

	var captured string
	old := Runner
	Runner = func(args []string) error {
		if len(args) >= 4 && args[1] == "send-text" {
			captured = args[3]
		}
		return nil
	}
	t.Cleanup(func() { Runner = old })

	envVars := map[string]string{
		"FLOW_TASK":    "my-task",
		"FLOW_PROJECT": "flow",
	}
	if err := SpawnTab("flow/my-task", "/Users/me/repo", "claude --resume abc", envVars); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	want := " FLOW_PROJECT='flow' FLOW_TASK='my-task' claude --resume abc\n"
	if captured != want {
		t.Errorf("send-text payload = %q; want %q", captured, want)
	}
}

// TestSpawnTabLaunchErrorShortCircuits — an error from `kitty @ launch`
// must be wrapped (with the remote-control hint) and send-text must NOT
// be attempted.
func TestSpawnTabLaunchErrorShortCircuits(t *testing.T) {
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return nil, errors.New("exit status 1: remote control is not enabled")
	})
	runnerCalled := false
	old := Runner
	Runner = func(args []string) error {
		runnerCalled = true
		return nil
	}
	t.Cleanup(func() { Runner = old })

	err := SpawnTab("t", "/tmp", "echo hi", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if runnerCalled {
		t.Error("Runner (send-text) should not be called when launch fails")
	}
	if !strings.Contains(err.Error(), "allow_remote_control") {
		t.Errorf("expected error to mention remote_control hint, got: %v", err)
	}
}

// TestSpawnTabLaunchEmptyOutput — if kitty @ launch returns empty
// stdout, SpawnTab must fail rather than send-text to id:"" (which
// kitty would interpret as match-all).
func TestSpawnTabLaunchEmptyOutput(t *testing.T) {
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return []byte("\n"), nil
	})
	runnerCalled := false
	old := Runner
	Runner = func(args []string) error {
		runnerCalled = true
		return nil
	}
	t.Cleanup(func() { Runner = old })

	err := SpawnTab("t", "/tmp", "echo hi", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if runnerCalled {
		t.Error("Runner (send-text) should not be called when launch returns empty id")
	}
}

// TestSpawnTabFlattensEmbeddedNewlines — embedded `\n` in command must
// be replaced with a space before send-text, same reasoning as zellij
// (PTY would interpret each \n as Enter and run partial lines).
func TestSpawnTabFlattensEmbeddedNewlines(t *testing.T) {
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return []byte("5\n"), nil
	})
	var captured string
	old := Runner
	Runner = func(args []string) error {
		if len(args) >= 4 && args[1] == "send-text" {
			captured = args[3]
		}
		return nil
	}
	t.Cleanup(func() { Runner = old })

	cmd := "claude --session-id abc 'line1\nline2\nline3'"
	if err := SpawnTab("t", "/tmp", cmd, nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	want := " claude --session-id abc 'line1 line2 line3'\n"
	if captured != want {
		t.Errorf("send-text payload = %q; want %q", captured, want)
	}
	if got := strings.Count(captured, "\n"); got != 1 {
		t.Errorf("send-text payload contains %d newlines; want exactly 1", got)
	}
}

// TestSpawnTabShortCommandDoesNotWriteScript — the short-command path
// must stay byte-for-byte identical to the pre-script behaviour and
// never touch the filesystem.
func TestSpawnTabShortCommandDoesNotWriteScript(t *testing.T) {
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return []byte("42\n"), nil
	})
	oldWrite := WriteScript
	WriteScript = func(body string) (string, error) {
		t.Fatalf("WriteScript should not be called for a short command; body = %q", body)
		return "", nil
	}
	t.Cleanup(func() { WriteScript = oldWrite })

	var captured string
	old := Runner
	Runner = func(args []string) error {
		captured = args[3]
		return nil
	}
	t.Cleanup(func() { Runner = old })

	if err := SpawnTab("t", "/tmp", "echo hi", map[string]string{"A": "b"}); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if want := " A='b' echo hi\n"; captured != want {
		t.Errorf("send-text payload = %q; want %q", captured, want)
	}
}

// TestSpawnTabLongCommandUsesScript — a line that would overflow the
// tty's canonical input buffer (MAX_CANON, 1024 bytes on macOS) is
// written to a self-deleting script, and only ` bash '<path>'` is
// typed. The script keeps the command RAW (newlines and quotes
// intact) and carries env vars via `exec env`.
func TestSpawnTabLongCommandUsesScript(t *testing.T) {
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return []byte("42\n"), nil
	})

	const scriptPath = "/tmp/flow-kitty-0123456789abcdef0123456789abcdef.sh"
	var bodies []string
	oldWrite := WriteScript
	WriteScript = func(body string) (string, error) {
		bodies = append(bodies, body)
		return scriptPath, nil
	}
	t.Cleanup(func() { WriteScript = oldWrite })

	var captured string
	old := Runner
	Runner = func(args []string) error {
		captured = args[3]
		return nil
	}
	t.Cleanup(func() { Runner = old })

	prompt := "it's a long prompt\n" + strings.Repeat("x", 2000)
	command := "claude " + ShellQuote(prompt)
	envVars := map[string]string{"FLOW_ROOT": "/very/long/root"}

	if err := SpawnTab("t", "/tmp", command, envVars); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}

	if len(bodies) != 1 {
		t.Fatalf("WriteScript called %d times; want 1", len(bodies))
	}
	body := bodies[0]
	if wantPrefix := "#!/bin/bash\nrm -- \"$0\"\n"; !strings.HasPrefix(body, wantPrefix) {
		t.Errorf("script body prefix = %q; want %q", body, wantPrefix)
	}
	if !strings.Contains(body, "exec env FLOW_ROOT=") {
		t.Errorf("script body missing `exec env FLOW_ROOT=`; got:\n%s", body)
	}
	if !strings.Contains(body, command) {
		t.Errorf("script body does not contain the raw command; got:\n%s", body)
	}
	if !strings.Contains(body, `'\''`) {
		t.Errorf("script body should preserve the command's single quote (shell-escaped); got:\n%s", body)
	}
	// Three prefix/exec line terminators plus the raw newline kept
	// inside the quoted argument.
	if got := strings.Count(body, "\n"); got != 4 {
		t.Errorf("script body has %d newlines; want 4 (raw command newline preserved)", got)
	}
	if want := " bash " + ShellQuote(scriptPath) + "\n"; captured != want {
		t.Errorf("send-text payload = %q; want %q", captured, want)
	}
}

// TestSpawnTabLongCommandLaunchErrorRemovesScript — if `kitty @ launch`
// fails after the script was written, the orphaned temp file must be
// removed and send-text must not run.
func TestSpawnTabLongCommandLaunchErrorRemovesScript(t *testing.T) {
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return nil, errors.New("exit status 1: remote control is not enabled")
	})

	const scriptPath = "/tmp/flow-kitty-0123456789abcdef0123456789abcdef.sh"
	oldWrite := WriteScript
	WriteScript = func(body string) (string, error) { return scriptPath, nil }
	t.Cleanup(func() { WriteScript = oldWrite })

	var removed []string
	oldRemove := removeScript
	removeScript = func(path string) error {
		removed = append(removed, path)
		return nil
	}
	t.Cleanup(func() { removeScript = oldRemove })

	runnerCalled := false
	old := Runner
	Runner = func(args []string) error {
		runnerCalled = true
		return nil
	}
	t.Cleanup(func() { Runner = old })

	long := "echo " + ShellQuote(strings.Repeat("y", 2000))
	if err := SpawnTab("t", "/tmp", long, nil); err == nil {
		t.Fatal("expected error, got nil")
	}
	if runnerCalled {
		t.Error("Runner (send-text) should not be called when launch fails")
	}
	if !slices.Equal(removed, []string{scriptPath}) {
		t.Errorf("removeScript calls = %v; want [%q]", removed, scriptPath)
	}
}

// TestWriteScriptDefaultPathAndPermissions pins the temp-file contract:
// flow-kitty-<32 hex>.sh under os.TempDir(), mode 0700.
func TestWriteScriptDefaultPathAndPermissions(t *testing.T) {
	path, err := WriteScript("#!/bin/bash\nexit 0\n")
	if err != nil {
		t.Fatalf("WriteScript: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	if dir := filepath.Dir(path); dir != filepath.Clean(os.TempDir()) {
		t.Errorf("script dir = %q; want %q", dir, filepath.Clean(os.TempDir()))
	}
	base := filepath.Base(path)
	const prefix, suffix = "flow-kitty-", ".sh"
	if !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, suffix) {
		t.Errorf("script name = %q; want %s<32 hex>%s", base, prefix, suffix)
	}
	if hexLen := len(base) - len(prefix) - len(suffix); hexLen != 32 {
		t.Errorf("hex id length = %d; want 32 (name %q)", hexLen, base)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat script: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("script mode = %o; want 700", perm)
	}
}

func stubRunnerOutput(t *testing.T, fn func([]string) ([]byte, error)) {
	t.Helper()
	old := RunnerOutput
	RunnerOutput = fn
	t.Cleanup(func() { RunnerOutput = old })
}

// TestFocusSessionEmptyID short-circuits without invoking kitty.
func TestFocusSessionEmptyID(t *testing.T) {
	rCalled := false
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		rCalled = true
		return nil, nil
	})
	focused, err := FocusSession("", "claude")
	if focused || err != nil {
		t.Errorf("FocusSession(\"\") = (%v, %v); want (false, nil)", focused, err)
	}
	if rCalled {
		t.Error("RunnerOutput should not be called for empty session id")
	}
}

// TestFocusSessionMatchesAndFocuses verifies the happy path: a
// foreground process under a kitty window runs claude with the target
// UUID, and FocusSession issues `kitty @ focus-window --match=id:<n>`
// for that window.
func TestFocusSessionMatchesAndFocuses(t *testing.T) {
	const uuid = "c18a6fe7-7cb0-4875-93d5-6ad1e9785763"
	listJSON := `[
	  {"tabs": [
	    {"windows": [
	      {"id": 3, "foreground_processes": [{"cmdline": ["/opt/homebrew/bin/fish"]}]},
	      {"id": 7, "foreground_processes": [
	        {"cmdline": ["/opt/homebrew/bin/fish"]},
	        {"cmdline": ["claude", "--session-id", "c18a6fe7-7cb0-4875-93d5-6ad1e9785763", "You are the execution session"]}
	      ]}
	    ]}
	  ]}
	]`
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		want := []string{"@", "ls"}
		if !slices.Equal(args, want) {
			t.Errorf("RunnerOutput args = %v; want %v", args, want)
		}
		return []byte(listJSON), nil
	})

	var focusArgs []string
	old := Runner
	Runner = func(args []string) error {
		focusArgs = append([]string(nil), args...)
		return nil
	}
	t.Cleanup(func() { Runner = old })

	focused, err := FocusSession(uuid, "claude")
	if err != nil {
		t.Fatalf("FocusSession: %v", err)
	}
	if !focused {
		t.Fatal("expected focused=true")
	}
	want := []string{"@", "focus-window", "--match=id:7"}
	if !slices.Equal(focusArgs, want) {
		t.Errorf("focus argv = %v; want %v", focusArgs, want)
	}
}

// TestFocusSessionResumeFlag covers `--resume <uuid>` in argv.
func TestFocusSessionResumeFlag(t *testing.T) {
	const uuid = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	listJSON := `[
	  {"tabs": [
	    {"windows": [
	      {"id": 11, "foreground_processes": [
	        {"cmdline": ["claude", "--resume", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"]}
	      ]}
	    ]}
	  ]}
	]`
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte(listJSON), nil })
	var focusArgs []string
	old := Runner
	Runner = func(args []string) error { focusArgs = args; return nil }
	t.Cleanup(func() { Runner = old })

	focused, err := FocusSession(uuid, "claude")
	if err != nil || !focused {
		t.Fatalf("got (%v, %v); want (true, nil)", focused, err)
	}
	want := []string{"@", "focus-window", "--match=id:11"}
	if !slices.Equal(focusArgs, want) {
		t.Errorf("focus argv = %v; want %v", focusArgs, want)
	}
}

// TestFocusSessionUUIDCaseInsensitive — UUID match is case-insensitive,
// matching the iterm / zellij behaviour.
func TestFocusSessionUUIDCaseInsensitive(t *testing.T) {
	const uuid = "AAAAAAAA-BBBB-4CCC-8DDD-EEEEEEEEEEEE"
	listJSON := `[
	  {"tabs": [
	    {"windows": [
	      {"id": 9, "foreground_processes": [
	        {"cmdline": ["claude", "--session-id", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"]}
	      ]}
	    ]}
	  ]}
	]`
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte(listJSON), nil })
	old := Runner
	Runner = func(args []string) error { return nil }
	t.Cleanup(func() { Runner = old })

	focused, err := FocusSession(uuid, "claude")
	if err != nil || !focused {
		t.Errorf("uppercase UUID should match lowercase cmdline; got (%v, %v)", focused, err)
	}
}

// TestFocusSessionOpencodeShortFlag covers opencode's `-s <ses_…>`
// argv shape, which the UUID regex doesn't recognize.
func TestFocusSessionOpencodeShortFlag(t *testing.T) {
	const sid = "ses_0123456789ABCDEFGHIJKLMNOP"
	listJSON := `[
	  {"tabs": [
	    {"windows": [
	      {"id": 21, "foreground_processes": [
	        {"cmdline": ["opencode", "-s", "ses_0123456789ABCDEFGHIJKLMNOP", "--prompt", "hello"]}
	      ]}
	    ]}
	  ]}
	]`
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte(listJSON), nil })
	var focusArgs []string
	old := Runner
	Runner = func(args []string) error { focusArgs = append([]string(nil), args...); return nil }
	t.Cleanup(func() { Runner = old })

	focused, err := FocusSession(sid, "opencode")
	if err != nil || !focused {
		t.Fatalf("got (%v, %v); want (true, nil)", focused, err)
	}
	want := []string{"@", "focus-window", "--match=id:21"}
	if !slices.Equal(focusArgs, want) {
		t.Errorf("focus argv = %v; want %v", focusArgs, want)
	}
}

// TestFocusSessionOpencodeLongFlagAndCaseSensitive covers `--session=<id>`
// and pins the case-SENSITIVE compare for opencode ids (unlike UUIDs).
func TestFocusSessionOpencodeLongFlagAndCaseSensitive(t *testing.T) {
	const lower = "ses_abcdefghijklmnopqrstuvwxyz"
	const upper = "ses_ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	listJSON := `[
	  {"tabs": [
	    {"windows": [
	      {"id": 22, "foreground_processes": [
	        {"cmdline": ["opencode", "--session=` + upper + `"]}
	      ]}
	    ]}
	  ]}
	]`
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte(listJSON), nil })
	old := Runner
	focusedCalls := 0
	Runner = func(args []string) error { focusedCalls++; return nil }
	t.Cleanup(func() { Runner = old })

	if focused, err := FocusSession(lower, "opencode"); err != nil || focused {
		t.Errorf("lowercase id must not match uppercase argv; got (%v, %v)", focused, err)
	}
	if focusedCalls != 0 {
		t.Errorf("focus-window called %d times on a case mismatch, want 0", focusedCalls)
	}
	if focused, err := FocusSession(upper, "opencode"); err != nil || !focused {
		t.Errorf("exact id should match --session=<id>; got (%v, %v)", focused, err)
	}
}

// TestFocusSessionAcrossOSWindows — the match scan walks every OS
// window, not just the first.
func TestFocusSessionAcrossOSWindows(t *testing.T) {
	const uuid = "11111111-2222-4333-8444-555555555555"
	listJSON := `[
	  {"tabs": [
	    {"windows": [{"id": 1, "foreground_processes": [{"cmdline": ["/bin/zsh"]}]}]}
	  ]},
	  {"tabs": [
	    {"windows": [
	      {"id": 22, "foreground_processes": [
	        {"cmdline": ["claude", "--session-id", "11111111-2222-4333-8444-555555555555"]}
	      ]}
	    ]}
	  ]}
	]`
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte(listJSON), nil })
	var focusArgs []string
	old := Runner
	Runner = func(args []string) error { focusArgs = args; return nil }
	t.Cleanup(func() { Runner = old })

	focused, err := FocusSession(uuid, "claude")
	if err != nil || !focused {
		t.Fatalf("got (%v, %v); want (true, nil)", focused, err)
	}
	want := []string{"@", "focus-window", "--match=id:22"}
	if !slices.Equal(focusArgs, want) {
		t.Errorf("focus argv = %v; want %v", focusArgs, want)
	}
}

// TestFocusSessionNoMatch — no foreground process under any window
// runs claude with the target UUID; Runner must not be called.
func TestFocusSessionNoMatch(t *testing.T) {
	listJSON := `[
	  {"tabs": [
	    {"windows": [
	      {"id": 2, "foreground_processes": [{"cmdline": ["/opt/homebrew/bin/fish"]}]},
	      {"id": 4, "foreground_processes": [{"cmdline": ["claude"]}]}
	    ]}
	  ]}
	]`
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte(listJSON), nil })
	rCalled := false
	old := Runner
	Runner = func(args []string) error { rCalled = true; return nil }
	t.Cleanup(func() { Runner = old })

	focused, err := FocusSession("11111111-2222-4333-8444-555555555555", "claude")
	if focused || err != nil {
		t.Errorf("got (%v, %v); want (false, nil)", focused, err)
	}
	if rCalled {
		t.Error("Runner should not be called when no window matches")
	}
}

// TestFocusSessionSkipsWindowsWithoutClaude — windows whose
// foreground_processes do not include the substring `claude` are
// skipped without running the regex (cheap fast-path).
func TestFocusSessionSkipsWindowsWithoutClaude(t *testing.T) {
	listJSON := `[
	  {"tabs": [
	    {"windows": [
	      {"id": 5, "foreground_processes": [{"cmdline": ["vim", "--session-id", "11111111-2222-4333-8444-555555555555"]}]}
	    ]}
	  ]}
	]`
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte(listJSON), nil })
	rCalled := false
	old := Runner
	Runner = func(args []string) error { rCalled = true; return nil }
	t.Cleanup(func() { Runner = old })

	// Even though the cmdline contains a UUID that matches the regex,
	// the absence of `claude` in the joined cmdline must short-circuit.
	focused, err := FocusSession("11111111-2222-4333-8444-555555555555", "claude")
	if focused || err != nil {
		t.Errorf("got (%v, %v); want (false, nil)", focused, err)
	}
	if rCalled {
		t.Error("Runner should not be called for non-claude foreground processes")
	}
}

// TestFocusSessionLsError surfaces a `kitty @ ls` failure as a
// backend error (distinct from "no match found"), wrapped with the
// command context.
func TestFocusSessionLsError(t *testing.T) {
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return nil, errors.New("exit status 1: remote control is not enabled")
	})
	focused, err := FocusSession("11111111-2222-4333-8444-555555555555", "claude")
	if focused || err == nil {
		t.Errorf("got (%v, %v); want (false, non-nil)", focused, err)
	}
	if !strings.Contains(err.Error(), "kitty @ ls") {
		t.Errorf("expected error to mention `kitty @ ls`, got: %v", err)
	}
}

// TestFocusSessionFocusError — a focus-window failure surfaces as a
// backend error, not as "no match".
func TestFocusSessionFocusError(t *testing.T) {
	const uuid = "11111111-2222-4333-8444-555555555555"
	listJSON := `[
	  {"tabs": [
	    {"windows": [
	      {"id": 13, "foreground_processes": [
	        {"cmdline": ["claude", "--session-id", "11111111-2222-4333-8444-555555555555"]}
	      ]}
	    ]}
	  ]}
	]`
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte(listJSON), nil })
	old := Runner
	Runner = func(args []string) error { return errors.New("kitty failed") }
	t.Cleanup(func() { Runner = old })

	focused, err := FocusSession(uuid, "claude")
	if focused || err == nil {
		t.Errorf("got (%v, %v); want (false, non-nil)", focused, err)
	}
	if !strings.Contains(err.Error(), "kitty @ focus-window") {
		t.Errorf("expected error to mention `kitty @ focus-window`, got: %v", err)
	}
}

// TestFocusSessionMalformedJSON — guard against kitty output drift.
func TestFocusSessionMalformedJSON(t *testing.T) {
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte("{not json"), nil })
	focused, err := FocusSession("11111111-2222-4333-8444-555555555555", "claude")
	if focused || err == nil {
		t.Errorf("got (%v, %v); want (false, non-nil) for malformed JSON", focused, err)
	}
}

// TestShellQuote — same contract as iterm/terminal/zellij.
func TestShellQuote(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain", "'plain'"},
		{"with space", "'with space'"},
		{"with'quote", `'with'\''quote'`},
	}
	for _, tc := range cases {
		if got := ShellQuote(tc.in); got != tc.want {
			t.Errorf("ShellQuote(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
