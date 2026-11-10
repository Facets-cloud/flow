package tmux

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// stubTmux replaces Runner, RunnerOutput and WriteScript with recorders.
// new-window returns paneID; every other tmux call succeeds. The script
// is "written" to scriptPath and its body is captured in *body.
func stubTmux(t *testing.T, paneID, scriptPath string) (calls *[][]string, body *string) {
	t.Helper()
	var c [][]string
	var b string
	oldR, oldRO, oldWS, oldRM := Runner, RunnerOutput, WriteScript, removeScript
	Runner = func(args []string) error {
		c = append(c, append([]string(nil), args...))
		return nil
	}
	RunnerOutput = func(args []string) ([]byte, error) {
		c = append(c, append([]string(nil), args...))
		return []byte(paneID + "\n"), nil
	}
	WriteScript = func(s string) (string, error) {
		b = s
		return scriptPath, nil
	}
	removeScript = func(string) error { return nil }
	t.Cleanup(func() { Runner, RunnerOutput, WriteScript, removeScript = oldR, oldRO, oldWS, oldRM })
	return &c, &b
}

// TestSpawnTabBasicNoEnv verifies the three-call argv sequence:
//  1. tmux new-window -P -F '#{pane_id}' -n <title> -c <cwd>   (prints pane id)
//  2. tmux send-keys -t <pane> -l " sh '<script>'"            (literal, histignorespace)
//  3. tmux send-keys -t <pane> Enter
//
// The command itself never goes through send-keys: it lives in the
// per-spawn script, so the typed line is a few dozen bytes no matter
// how long the bootstrap prompt is.
func TestSpawnTabBasicNoEnv(t *testing.T) {
	calls, body := stubTmux(t, "%7", "/tmp/flow-tmux-abc.sh")
	if err := SpawnTab("my-task", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	want := [][]string{
		{"new-window", "-P", "-F", "#{pane_id}", "-n", "my-task", "-c", "/tmp"},
		{"send-keys", "-t", "%7", "-l", " sh '/tmp/flow-tmux-abc.sh'"},
		{"send-keys", "-t", "%7", "Enter"},
	}
	if len(*calls) != len(want) {
		t.Fatalf("calls = %v; want %v", *calls, want)
	}
	for i := range want {
		if !slices.Equal((*calls)[i], want[i]) {
			t.Errorf("call[%d] = %v; want %v", i, (*calls)[i], want[i])
		}
	}
	wantBody := "#!/bin/sh\nrm -- \"$0\"\nexec echo hi\n"
	if *body != wantBody {
		t.Errorf("script body = %q; want %q", *body, wantBody)
	}
}

// TestSpawnTabEnvVarsSorted — env vars are emitted alphabetically, each
// value shell-quoted, as an `exec env` prefix inside the script (same
// contract as the warp backend's script and every other backend's line).
func TestSpawnTabEnvVarsSorted(t *testing.T) {
	_, body := stubTmux(t, "%1", "/tmp/s.sh")
	env := map[string]string{"FLOW_ROOT": "/home/me/.flow", "AAA": "it's"}
	if err := SpawnTab("t", "/repo", "claude --resume abc", env); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	want := "#!/bin/sh\nrm -- \"$0\"\nexec env AAA='it'\\''s' FLOW_ROOT='/home/me/.flow' claude --resume abc\n"
	if *body != want {
		t.Errorf("script body = %q; want %q", *body, want)
	}
}

// TestSpawnTabTypedLineIsShort is the regression test for the macOS
// tty line limit (MAX_CANON = 1024 bytes in canonical mode): a 1.2 KB
// launch line typed into a zsh that had not switched to raw mode yet
// was cut inside the quoted prompt and the window sat at `quote>`. The
// typed line must stay well under that limit — and under 256 bytes so
// there is headroom for long $TMPDIR paths — regardless of how large
// the command, prompt or env block is, and it must not contain the
// command at all.
func TestSpawnTabTypedLineIsShort(t *testing.T) {
	calls, body := stubTmux(t, "%3", "/var/folders/zz/zyxvpxvq6csfxvn_n0000000000000/T/flow-tmux-0123456789abcdef0123456789abcdef.sh")
	prompt := strings.Repeat("Invoke the flow skill via the Skill tool. ", 200) // ~8.4 KB
	command := "claude --session-id 11111111-2222-4333-8444-555555555555 '" + prompt + "'"
	env := map[string]string{"FLOW_ROOT": strings.Repeat("/very/long/path", 20)}
	if err := SpawnTab("t", "/repo", command, env); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	line := (*calls)[1][4]
	if len(line) >= 256 {
		t.Errorf("typed line is %d bytes; must be < 256: %q", len(line), line)
	}
	if strings.Contains(line, "claude") || strings.Contains(line, "FLOW_ROOT") {
		t.Errorf("typed line must not carry the command or env, got %q", line)
	}
	if strings.ContainsAny(line, "\n\r") {
		t.Errorf("typed line must be a single line, got %q", line)
	}
	if !strings.Contains(*body, command) || !strings.Contains(*body, "FLOW_ROOT=") {
		t.Errorf("script body must carry the full command and env; got %q", *body)
	}
}

// TestSpawnTabPreservesEmbeddedNewlines — the command lives in a script
// file now, so embedded newlines inside a quoted prompt survive intact
// (the old send-keys path had to flatten them). The typed line itself
// still has none.
func TestSpawnTabPreservesEmbeddedNewlines(t *testing.T) {
	calls, body := stubTmux(t, "%1", "/tmp/s.sh")
	if err := SpawnTab("t", "/tmp", "claude --session-id abc 'l1\nl2\nl3'", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if !strings.Contains(*body, "exec claude --session-id abc 'l1\nl2\nl3'\n") {
		t.Errorf("script body lost the newlines: %q", *body)
	}
	if strings.Contains((*calls)[1][4], "\n") {
		t.Errorf("typed line must not contain newlines: %q", (*calls)[1][4])
	}
}

// TestSpawnTabQuotesScriptPath — a temp dir with a space or quote in it
// must still produce a line sh can parse.
func TestSpawnTabQuotesScriptPath(t *testing.T) {
	calls, _ := stubTmux(t, "%1", "/tmp/odd dir/it's.sh")
	if err := SpawnTab("t", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if got, want := (*calls)[1][4], ` sh '/tmp/odd dir/it'\''s.sh'`; got != want {
		t.Errorf("typed line = %q; want %q", got, want)
	}
}

// TestSpawnTabPropagatesNewWindowError — a failing new-window returns
// the error, no send-keys is attempted, and the script written before
// it is removed again.
func TestSpawnTabPropagatesNewWindowError(t *testing.T) {
	var sends int
	var removed []string
	oldR, oldRO, oldWS, oldRM := Runner, RunnerOutput, WriteScript, removeScript
	Runner = func(args []string) error { sends++; return nil }
	RunnerOutput = func(args []string) ([]byte, error) {
		return nil, errors.New("no server running")
	}
	WriteScript = func(string) (string, error) { return "/tmp/s.sh", nil }
	removeScript = func(p string) error { removed = append(removed, p); return nil }
	t.Cleanup(func() { Runner, RunnerOutput, WriteScript, removeScript = oldR, oldRO, oldWS, oldRM })

	err := SpawnTab("t", "/tmp", "echo hi", nil)
	if err == nil || !strings.Contains(err.Error(), "no server running") {
		t.Fatalf("expected wrapped new-window error, got %v", err)
	}
	if sends != 0 {
		t.Errorf("send-keys should not run after new-window failed; ran %d times", sends)
	}
	if !slices.Equal(removed, []string{"/tmp/s.sh"}) {
		t.Errorf("orphaned script not removed; removed = %v", removed)
	}
}

// TestSpawnTabSendKeysErrorRemovesScript — if typing the line fails the
// script would never run (and never delete itself), so SpawnTab does.
func TestSpawnTabSendKeysErrorRemovesScript(t *testing.T) {
	var removed []string
	oldR, oldRO, oldWS, oldRM := Runner, RunnerOutput, WriteScript, removeScript
	Runner = func(args []string) error { return errors.New("pane gone") }
	RunnerOutput = func(args []string) ([]byte, error) { return []byte("%9\n"), nil }
	WriteScript = func(string) (string, error) { return "/tmp/s.sh", nil }
	removeScript = func(p string) error { removed = append(removed, p); return nil }
	t.Cleanup(func() { Runner, RunnerOutput, WriteScript, removeScript = oldR, oldRO, oldWS, oldRM })

	if err := SpawnTab("t", "/tmp", "echo hi", nil); err == nil {
		t.Fatal("expected send-keys error")
	}
	if !slices.Equal(removed, []string{"/tmp/s.sh"}) {
		t.Errorf("script not removed after send-keys failure; removed = %v", removed)
	}
}

// TestSpawnTabWriteScriptErrorOpensNoWindow — a failed script write is
// reported before any tmux call, so no empty window is left behind.
func TestSpawnTabWriteScriptErrorOpensNoWindow(t *testing.T) {
	var tmuxCalls int
	oldR, oldRO, oldWS := Runner, RunnerOutput, WriteScript
	Runner = func(args []string) error { tmuxCalls++; return nil }
	RunnerOutput = func(args []string) ([]byte, error) { tmuxCalls++; return []byte("%1\n"), nil }
	WriteScript = func(string) (string, error) { return "", errors.New("disk full") }
	t.Cleanup(func() { Runner, RunnerOutput, WriteScript = oldR, oldRO, oldWS })

	err := SpawnTab("t", "/tmp", "echo hi", nil)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("expected write error, got %v", err)
	}
	if tmuxCalls != 0 {
		t.Errorf("tmux must not be invoked when the script could not be written; got %d calls", tmuxCalls)
	}
}

// TestSpawnTabEmptyPaneID — an empty pane id from new-window is an
// error rather than a send-keys into "whatever is focused".
func TestSpawnTabEmptyPaneID(t *testing.T) {
	stubTmux(t, "", "/tmp/s.sh")
	if err := SpawnTab("t", "/tmp", "echo hi", nil); err == nil {
		t.Fatal("expected error for empty pane id")
	}
}

// TestWriteScriptRealFile — the default WriteScript puts a private
// (0700), self-deleting script under os.TempDir() with a flow-tmux-
// prefix, and the body is written verbatim.
func TestWriteScriptRealFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	path, err := WriteScript("#!/bin/sh\nexit 0\n")
	if err != nil {
		t.Fatalf("WriteScript: %v", err)
	}
	t.Cleanup(func() { _ = removeScript(path) })
	if !strings.HasPrefix(path, dir) || !strings.Contains(path, "flow-tmux-") || !strings.HasSuffix(path, ".sh") {
		t.Errorf("unexpected script path %q", path)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Errorf("script mode = %o; want 0700", st.Mode().Perm())
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "#!/bin/sh\nexit 0\n" {
		t.Errorf("script body = %q, err=%v", got, err)
	}
}

// TestFocusSessionFound — ps shows the harness on pts/3, list-panes maps
// /dev/pts/3 to pane %4, so select-window + select-pane target %4.
func TestFocusSessionFound(t *testing.T) {
	const sid = "11111111-2222-4333-8444-555555555555"
	var calls [][]string
	oldR, oldRO, oldPS := Runner, RunnerOutput, PSRunner
	Runner = func(args []string) error {
		calls = append(calls, append([]string(nil), args...))
		return nil
	}
	RunnerOutput = func(args []string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		return []byte("%2 /dev/pts/1\n%4 /dev/pts/3\n"), nil
	}
	PSRunner = func() ([]byte, error) {
		return []byte("  PID TTY      COMMAND\n" +
			"  100 pts/1    -bash\n" +
			"  200 pts/3    claude --session-id " + sid + " 'prompt'\n"), nil
	}
	t.Cleanup(func() { Runner, RunnerOutput, PSRunner = oldR, oldRO, oldPS })

	ok, err := FocusSession(sid, "claude")
	if err != nil || !ok {
		t.Fatalf("FocusSession = (%v, %v), want (true, nil)", ok, err)
	}
	want := [][]string{
		{"list-panes", "-a", "-F", "#{pane_id} #{pane_tty}"},
		{"select-window", "-t", "%4"},
		{"select-pane", "-t", "%4"},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v; want %v", calls, want)
	}
	for i := range want {
		if !slices.Equal(calls[i], want[i]) {
			t.Errorf("call[%d] = %v; want %v", i, calls[i], want[i])
		}
	}
}

// TestFocusSessionMiss — no matching process means (false, nil) and no
// tmux call at all.
func TestFocusSessionMiss(t *testing.T) {
	var tmuxCalls int
	oldR, oldRO, oldPS := Runner, RunnerOutput, PSRunner
	Runner = func(args []string) error { tmuxCalls++; return nil }
	RunnerOutput = func(args []string) ([]byte, error) { tmuxCalls++; return []byte(""), nil }
	PSRunner = func() ([]byte, error) { return []byte("  PID TTY COMMAND\n 1 pts/0 -zsh\n"), nil }
	t.Cleanup(func() { Runner, RunnerOutput, PSRunner = oldR, oldRO, oldPS })

	ok, err := FocusSession("11111111-2222-4333-8444-555555555555", "claude")
	if ok || err != nil {
		t.Fatalf("FocusSession = (%v, %v), want (false, nil)", ok, err)
	}
	if tmuxCalls != 0 {
		t.Errorf("tmux should not be invoked without a ps match; got %d calls", tmuxCalls)
	}
}

// TestFocusSessionEmptyID short-circuits.
func TestFocusSessionEmptyID(t *testing.T) {
	oldPS := PSRunner
	PSRunner = func() ([]byte, error) { t.Fatal("ps must not run for empty session id"); return nil, nil }
	t.Cleanup(func() { PSRunner = oldPS })
	if ok, err := FocusSession("", "claude"); ok || err != nil {
		t.Fatalf("got (%v, %v), want (false, nil)", ok, err)
	}
}
