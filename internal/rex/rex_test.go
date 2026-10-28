package rex

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func stubRunnerOutput(t *testing.T, fn func(args []string) ([]byte, error)) {
	t.Helper()
	old := RunnerOutput
	RunnerOutput = fn
	t.Cleanup(func() { RunnerOutput = old })
}

func stubRunner(t *testing.T, fn func(args []string) error) {
	t.Helper()
	old := Runner
	Runner = fn
	t.Cleanup(func() { Runner = old })
}

func stubProcessArgs(t *testing.T, fn func(pid int) (string, error)) {
	t.Helper()
	old := ProcessArgs
	ProcessArgs = fn
	t.Cleanup(func() { ProcessArgs = old })
}

const windowNewJSON = `{"window_id":"window:w1","block_ids":["block:b1"],"revision":3}`

// TestSpawnTabBasicNoEnv verifies the two-call argv sequence inside a
// Rex terminal:
//  1. RunnerOutput: rex window new <t> -s <session> -c <cwd> --json
//  2. Runner:       rex send -s <session> -b <block> " <command>\n"
func TestSpawnTabBasicNoEnv(t *testing.T) {
	t.Setenv("REX_SESSION", "session:s1")
	var newArgs, sendArgs []string
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		newArgs = append([]string(nil), args...)
		return []byte(windowNewJSON), nil
	})
	stubRunner(t, func(args []string) error {
		sendArgs = append([]string(nil), args...)
		return nil
	})

	if err := SpawnTab("my-task", "/tmp", "echo hi", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}

	wantNew := []string{"window", "new", "my-task", "-s", "session:s1", "-c", "/tmp", "--json"}
	if !slices.Equal(newArgs, wantNew) {
		t.Errorf("window new argv = %v; want %v", newArgs, wantNew)
	}
	wantSend := []string{"send", "-s", "session:s1", "-b", "block:b1", " echo hi\n"}
	if !slices.Equal(sendArgs, wantSend) {
		t.Errorf("send argv = %v; want %v", sendArgs, wantSend)
	}
}

// TestSpawnTabNoSession — with FLOW_TERM=rex from outside Rex there is no
// $REX_SESSION, so -s is omitted and Rex picks its default session.
func TestSpawnTabNoSession(t *testing.T) {
	t.Setenv("REX_SESSION", "")
	var newArgs, sendArgs []string
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		newArgs = append([]string(nil), args...)
		return []byte(windowNewJSON), nil
	})
	stubRunner(t, func(args []string) error {
		sendArgs = append([]string(nil), args...)
		return nil
	})

	if err := SpawnTab("t", "/w", "x", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if slices.Contains(newArgs, "-s") || slices.Contains(sendArgs, "-s") {
		t.Errorf("expected no -s without REX_SESSION; got new=%v send=%v", newArgs, sendArgs)
	}
}

// TestSpawnTabEnvVarsSorted verifies env vars are emitted alphabetically,
// each value shell-quoted, before the command — the same env-prefix
// contract as the kitty/iterm/zellij backends.
func TestSpawnTabEnvVarsSorted(t *testing.T) {
	t.Setenv("REX_SESSION", "session:s1")
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return []byte(windowNewJSON), nil
	})
	var line string
	stubRunner(t, func(args []string) error {
		line = args[len(args)-1]
		return nil
	})

	env := map[string]string{"ZED": "z", "ALPHA": "it's"}
	if err := SpawnTab("t", "/w", "claude --resume x", env); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	want := ` ALPHA='it'\''s' ZED='z' claude --resume x` + "\n"
	if line != want {
		t.Errorf("send line = %q; want %q", line, want)
	}
}

// TestSpawnTabFlattensNewlines — embedded newlines would submit partial
// lines to the shell, so they become spaces.
func TestSpawnTabFlattensNewlines(t *testing.T) {
	t.Setenv("REX_SESSION", "session:s1")
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return []byte(windowNewJSON), nil
	})
	var line string
	stubRunner(t, func(args []string) error {
		line = args[len(args)-1]
		return nil
	})
	if err := SpawnTab("t", "/w", "a\nb\nc", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if line != " a b c\n" {
		t.Errorf("send line = %q; want %q", line, " a b c\n")
	}
}

func TestSpawnTabWindowNewError(t *testing.T) {
	t.Setenv("REX_SESSION", "session:s1")
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return nil, errors.New("exit status 4")
	})
	sent := false
	stubRunner(t, func(args []string) error { sent = true; return nil })

	err := SpawnTab("t", "/w", "x", nil)
	if err == nil || !strings.Contains(err.Error(), "rex window new") {
		t.Fatalf("want wrapped rex window new error, got %v", err)
	}
	if sent {
		t.Error("send must not run when window new fails")
	}
}

func TestSpawnTabNoBlockID(t *testing.T) {
	t.Setenv("REX_SESSION", "session:s1")
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return []byte(`{"window_id":"window:w1","block_ids":[]}`), nil
	})
	stubRunner(t, func(args []string) error { return nil })
	if err := SpawnTab("t", "/w", "x", nil); err == nil {
		t.Fatal("want error when rex returns no block id")
	}
}

const (
	uuidA = "2b7f3148-ba10-4b21-8592-5691a298ef7d"
	uuidB = "fc93f463-8d67-4d35-b5e3-b1cf48ebdce9"
)

// fakeRex answers list_blocks and per-block process calls for
// FocusSession tests. procs maps block id → process JSON.
func fakeRex(t *testing.T, blocks string, procs map[string]string) {
	t.Helper()
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		if len(args) == 2 && args[0] == "ls" {
			return []byte(`{"sessions":[{"session_id":"session:s1","label":"main"}]}`), nil
		}
		if len(args) < 3 || args[0] != "api" || args[1] != "call" {
			t.Fatalf("unexpected rex call %v", args)
		}
		switch args[2] {
		case "session.list_blocks":
			return []byte(blocks), nil
		case "com.superlogical.terminal.process":
			for id, js := range procs {
				if strings.Contains(args[3], id) {
					return []byte(js), nil
				}
			}
			return nil, errors.New("not a terminal")
		}
		t.Fatalf("unexpected method %v", args[2])
		return nil, nil
	})
}

const twoBlocks = `{"blocks":[
 {"block_id":"block:b1","window_id":"window:w1","placement":"placed"},
 {"block_id":"block:b2","window_id":"window:w2","placement":"placed"},
 {"block_id":"block:b3","placement":"detached"}]}`

func TestFocusSessionMatches(t *testing.T) {
	t.Setenv("REX_SESSION", "session:s1")
	fakeRex(t, twoBlocks, map[string]string{
		"block:b1": `{"foreground":{"pid":11,"name":"zsh","argv0":"-zsh"}}`,
		"block:b2": `{"foreground":{"pid":22,"name":"claude.exe","argv0":"claude"}}`,
	})
	stubProcessArgs(t, func(pid int) (string, error) {
		if pid != 22 {
			t.Errorf("ps should only run for claude blocks, got pid %d", pid)
		}
		return "claude --resume " + uuidA + " --dangerously-skip-permissions", nil
	})
	var focusArgs []string
	stubRunner(t, func(args []string) error {
		focusArgs = append([]string(nil), args...)
		return nil
	})

	ok, err := FocusSession(uuidA, "claude")
	if err != nil || !ok {
		t.Fatalf("FocusSession = (%v, %v); want (true, nil)", ok, err)
	}
	want := []string{"focus", "-s", "session:s1", "window:w2"}
	if !slices.Equal(focusArgs, want) {
		t.Errorf("focus argv = %v; want %v", focusArgs, want)
	}
}

func TestFocusSessionNoMatch(t *testing.T) {
	t.Setenv("REX_SESSION", "session:s1")
	fakeRex(t, twoBlocks, map[string]string{
		"block:b2": `{"foreground":{"pid":22,"name":"claude.exe","argv0":"claude"}}`,
	})
	stubProcessArgs(t, func(pid int) (string, error) {
		return "claude --session-id " + uuidB, nil
	})
	stubRunner(t, func(args []string) error {
		t.Errorf("focus must not run without a match: %v", args)
		return nil
	})
	ok, err := FocusSession(uuidA, "claude")
	if err != nil || ok {
		t.Fatalf("FocusSession = (%v, %v); want (false, nil)", ok, err)
	}
}

// TestFocusSessionOutsideRex — with FLOW_TERM=rex from outside Rex
// there is no current session; every session is scanned, and a tab
// found anywhere is brought into view.
func TestFocusSessionOutsideRex(t *testing.T) {
	t.Setenv("REX_SESSION", "")
	f := &fakeServer{t: t, sessions: threeSessions,
		blocks: map[string]string{
			"session:sm": `{"blocks":[{"block_id":"block:s1","window_id":"window:s1","placement":"placed"}]}`,
		},
		procs: map[string]string{"block:s1": claudeProc}}
	f.install()
	stubProcessArgs(t, func(pid int) (string, error) {
		return "claude --resume " + uuidA, nil
	})
	ok, err := FocusSession(uuidA, "claude")
	if err != nil || !ok {
		t.Fatalf("FocusSession = (%v, %v); want (true, nil)", ok, err)
	}
	// rex ls order is kept without a current session: personal is
	// scanned, then sessionm holds the match and the scan stops.
	if n := f.count("api", "call", "session.list_blocks"); n != 2 {
		t.Errorf("list_blocks calls = %d; want 2 (scan in order, stop at hit)", n)
	}
	if f.find("api", "call", "client.perform") == nil {
		t.Error("want the found session brought into view")
	}
}

func TestFocusSessionListError(t *testing.T) {
	t.Setenv("REX_SESSION", "session:s1")
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		return nil, errors.New("exit status 4")
	})
	if _, err := FocusSession(uuidA, "claude"); err == nil {
		t.Fatal("want error when list_blocks fails")
	}
}

func TestShellQuote(t *testing.T) {
	if got := ShellQuote("it's"); got != `'it'\''s'` {
		t.Errorf("ShellQuote = %q", got)
	}
}

// fakeServer is a scripted rex CLI for routing tests: a fixed session
// list, per-session block/process tables, and a recorder of every call.
type fakeServer struct {
	t           *testing.T
	sessions    string            // rex ls --json
	blocks      map[string]string // session id → list_blocks JSON
	procs       map[string]string // block id → process JSON
	performErr  error             // client.perform result
	calls       [][]string        // every RunnerOutput + Runner call
	newSessJSON string            // rex new --json
}

func (f *fakeServer) install() {
	f.t.Helper()
	stubRunnerOutput(f.t, func(args []string) ([]byte, error) {
		f.calls = append(f.calls, append([]string(nil), args...))
		switch {
		case args[0] == "ls":
			return []byte(f.sessions), nil
		case args[0] == "new":
			return []byte(f.newSessJSON), nil
		case args[0] == "window" && args[1] == "new":
			return []byte(windowNewJSON), nil
		case args[0] == "client" && args[1] == "ls":
			return []byte(`{"clients":[
			 {"client_id":"client:cli"},
			 {"client_id":"client:ios","info":{"kind":"app","platform":"ios"}},
			 {"client_id":"client:mac","info":{"kind":"app","platform":"macos"}}]}`), nil
		case args[0] == "api" && args[2] == "client.perform":
			return []byte(`{}`), f.performErr
		case args[0] == "api" && args[2] == "session.list_blocks":
			for sid, js := range f.blocks {
				if strings.Contains(args[3], sid) {
					return []byte(js), nil
				}
			}
			return []byte(`{"blocks":[]}`), nil
		case args[0] == "api" && args[2] == "com.superlogical.terminal.process":
			for id, js := range f.procs {
				if strings.Contains(args[3], `"`+id+`"`) {
					return []byte(js), nil
				}
			}
			return nil, errors.New("not a terminal")
		}
		f.t.Fatalf("unexpected rex call %v", args)
		return nil, nil
	})
	stubRunner(f.t, func(args []string) error {
		f.calls = append(f.calls, append([]string(nil), args...))
		return nil
	})
}

// find returns the first recorded call whose leading args match prefix.
func (f *fakeServer) find(prefix ...string) []string {
	for _, c := range f.calls {
		if len(c) >= len(prefix) && slices.Equal(c[:len(prefix)], prefix) {
			return c
		}
	}
	return nil
}

func (f *fakeServer) count(prefix ...string) int {
	n := 0
	for _, c := range f.calls {
		if len(c) >= len(prefix) && slices.Equal(c[:len(prefix)], prefix) {
			n++
		}
	}
	return n
}

const threeSessions = `{"sessions":[
 {"session_id":"session:cur","label":"personal"},
 {"session_id":"session:sm","label":"sessionm"},
 {"session_id":"session:fx","label":"facets"}]}`

func captureWarn(t *testing.T) *strings.Builder {
	t.Helper()
	var b strings.Builder
	old := Warn
	Warn = &b
	t.Cleanup(func() { Warn = old })
	return &b
}

// TestSpawnTabInExistingSession — the tab opens in the named session
// (by full id, so labels that look like positions can't misroute), the
// command is sent there, and only the macOS app is asked to show it.
func TestSpawnTabInExistingSession(t *testing.T) {
	t.Setenv("REX_SESSION", "session:cur")
	f := &fakeServer{t: t, sessions: threeSessions}
	f.install()
	warn := captureWarn(t)

	if err := SpawnTabIn("SessionM", "sessionm/x", "/w", "claude", nil); err != nil {
		t.Fatalf("SpawnTabIn: %v", err)
	}
	wantNew := []string{"window", "new", "sessionm/x", "-s", "session:sm", "-c", "/w", "--json"}
	if got := f.find("window", "new"); !slices.Equal(got, wantNew) {
		t.Errorf("window new = %v; want %v", got, wantNew)
	}
	wantSend := []string{"send", "-s", "session:sm", "-b", "block:b1", " claude\n"}
	if got := f.find("send"); !slices.Equal(got, wantSend) {
		t.Errorf("send = %v; want %v", got, wantSend)
	}
	if n := f.count("api", "call", "client.perform"); n != 1 {
		t.Fatalf("client.perform calls = %d; want 1 (mac app only)", n)
	}
	perform := f.find("api", "call", "client.perform")[3]
	for _, want := range []string{`"client:mac"`, `"session.select"`, `"session:sm"`, `"window:w1"`} {
		if !strings.Contains(perform, want) {
			t.Errorf("client.perform payload %s missing %s", perform, want)
		}
	}
	if warn.Len() != 0 {
		t.Errorf("unexpected warning: %q", warn.String())
	}
}

// TestSpawnTabInCurrentSession — routing to the session flow already
// runs in must not switch the app's view.
func TestSpawnTabInCurrentSession(t *testing.T) {
	t.Setenv("REX_SESSION", "session:cur")
	f := &fakeServer{t: t, sessions: threeSessions}
	f.install()
	if err := SpawnTabIn("personal", "t", "/w", "x", nil); err != nil {
		t.Fatalf("SpawnTabIn: %v", err)
	}
	if got := f.find("window", "new"); !slices.Contains(got, "session:cur") {
		t.Errorf("window new = %v; want -s session:cur", got)
	}
	if f.count("client", "ls") != 0 || f.count("api", "call", "client.perform") != 0 {
		t.Errorf("must not switch view for the current session; calls=%v", f.calls)
	}
}

// TestSpawnTabInEmptyTargetSkipsLookup — no mapping means the old
// behaviour exactly: no `rex ls`, no view switch.
func TestSpawnTabInEmptyTargetSkipsLookup(t *testing.T) {
	t.Setenv("REX_SESSION", "session:cur")
	f := &fakeServer{t: t, sessions: threeSessions}
	f.install()
	if err := SpawnTabIn("", "t", "/w", "x", nil); err != nil {
		t.Fatalf("SpawnTabIn: %v", err)
	}
	if f.count("ls") != 0 || f.count("client", "ls") != 0 {
		t.Errorf("empty target must not list sessions or clients; calls=%v", f.calls)
	}
}

// TestSpawnTabInCreatesMissingSession — an unknown label creates the
// session with the task's tab as its first window, sends the command to
// that window's block, and brings it into view.
func TestSpawnTabInCreatesMissingSession(t *testing.T) {
	t.Setenv("REX_SESSION", "session:cur")
	f := &fakeServer{t: t, sessions: threeSessions,
		newSessJSON: `{"session_id":"session:tk","initial_windows":[{"window_id":"window:tw","block_ids":["block:tb"]}]}`}
	f.install()

	if err := SpawnTabIn("tekion", "tekion/x", "/w", "claude", nil); err != nil {
		t.Fatalf("SpawnTabIn: %v", err)
	}
	wantNew := []string{"new", "tekion", "--cwd", "/w", "--window", "tekion/x", "--json"}
	if got := f.find("new"); !slices.Equal(got, wantNew) {
		t.Errorf("rex new = %v; want %v", got, wantNew)
	}
	if f.count("window", "new") != 0 {
		t.Error("must not also open a second tab with window new")
	}
	wantSend := []string{"send", "-s", "session:tk", "-b", "block:tb", " claude\n"}
	if got := f.find("send"); !slices.Equal(got, wantSend) {
		t.Errorf("send = %v; want %v", got, wantSend)
	}
	perform := f.find("api", "call", "client.perform")
	if perform == nil || !strings.Contains(perform[3], `"window:tw"`) {
		t.Errorf("client.perform = %v; want switch to window:tw", perform)
	}
}

// TestSpawnTabInRemoteControlOff — the app refusing to switch views is
// not a spawn failure: the tab exists, flow prints how to enable it.
func TestSpawnTabInRemoteControlOff(t *testing.T) {
	t.Setenv("REX_SESSION", "session:cur")
	f := &fakeServer{t: t, sessions: threeSessions,
		performErr: errors.New("exit status 1: Error: Rex.app is not accepting actions. Turn on Remote Control in its Settings under Rex Server. (status=403 code=refused)")}
	f.install()
	warn := captureWarn(t)

	if err := SpawnTabIn("facets", "t", "/w", "x", nil); err != nil {
		t.Fatalf("SpawnTabIn must succeed when only the view switch fails: %v", err)
	}
	if !strings.Contains(warn.String(), "Remote Control") || !strings.Contains(warn.String(), `"facets"`) {
		t.Errorf("warning = %q; want Remote Control hint naming session \"facets\"", warn.String())
	}
}

// TestSpawnTabInListError — failing to list sessions fails the spawn
// rather than silently dropping the tab somewhere unexpected.
func TestSpawnTabInListError(t *testing.T) {
	t.Setenv("REX_SESSION", "session:cur")
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		if args[0] == "ls" {
			return nil, errors.New("exit status 4")
		}
		t.Fatalf("unexpected call %v", args)
		return nil, nil
	})
	stubRunner(t, func(args []string) error {
		t.Fatalf("unexpected call %v", args)
		return nil
	})
	if err := SpawnTabIn("facets", "t", "/w", "x", nil); err == nil {
		t.Fatal("want error when rex ls fails")
	}
}

const claudeProc = `{"foreground":{"pid":22,"name":"claude.exe","argv0":"claude"}}`

// TestFocusSessionOtherSession — a tab living in another Rex session is
// found, focused there, and brought into view.
func TestFocusSessionOtherSession(t *testing.T) {
	t.Setenv("REX_SESSION", "session:cur")
	f := &fakeServer{t: t, sessions: threeSessions,
		blocks: map[string]string{
			"session:cur": `{"blocks":[{"block_id":"block:c1","window_id":"window:c1","placement":"placed"}]}`,
			"session:fx":  `{"blocks":[{"block_id":"block:f1","window_id":"window:f1","placement":"placed"}]}`,
		},
		procs: map[string]string{
			"block:c1": `{"foreground":{"pid":11,"name":"zsh","argv0":"-zsh"}}`,
			"block:f1": claudeProc,
		}}
	f.install()
	stubProcessArgs(t, func(pid int) (string, error) {
		return "claude --resume " + uuidA, nil
	})

	ok, err := FocusSession(uuidA, "claude")
	if err != nil || !ok {
		t.Fatalf("FocusSession = (%v, %v); want (true, nil)", ok, err)
	}
	want := []string{"focus", "-s", "session:fx", "window:f1"}
	if got := f.find("focus"); !slices.Equal(got, want) {
		t.Errorf("focus = %v; want %v", got, want)
	}
	perform := f.find("api", "call", "client.perform")
	if perform == nil || !strings.Contains(perform[3], `"session:fx"`) {
		t.Errorf("client.perform = %v; want switch to session:fx", perform)
	}
}

// TestFocusSessionCurrentFirst — when the current session holds the
// tab, it is scanned first and no view switch happens, even if it is
// listed last by rex ls.
func TestFocusSessionCurrentFirst(t *testing.T) {
	t.Setenv("REX_SESSION", "session:fx")
	f := &fakeServer{t: t, sessions: threeSessions,
		blocks: map[string]string{
			"session:fx": `{"blocks":[{"block_id":"block:f1","window_id":"window:f1","placement":"placed"}]}`,
		},
		procs: map[string]string{"block:f1": claudeProc}}
	f.install()
	stubProcessArgs(t, func(pid int) (string, error) {
		return "claude --resume " + uuidA, nil
	})

	ok, err := FocusSession(uuidA, "claude")
	if err != nil || !ok {
		t.Fatalf("FocusSession = (%v, %v); want (true, nil)", ok, err)
	}
	if n := f.count("api", "call", "session.list_blocks"); n != 1 {
		t.Errorf("list_blocks calls = %d; want 1 (current session first, then stop)", n)
	}
	if f.count("api", "call", "client.perform") != 0 {
		t.Error("must not switch view when the tab is in the current session")
	}
}

// stubScripts records spawn scripts in memory instead of the temp dir.
func stubScripts(t *testing.T) (written map[string]string, removed *[]string) {
	t.Helper()
	written = map[string]string{}
	removed = new([]string)
	oldP, oldW, oldR := NewScriptPath, WriteScript, removeScript
	n := 0
	NewScriptPath = func() (string, error) {
		n++
		return filepath.Join("/tmp", "flow-rex-test-"+string(rune('a'+n))+".sh"), nil
	}
	WriteScript = func(path, body string) error { written[path] = body; return nil }
	removeScript = func(path string) error { *removed = append(*removed, path); return nil }
	t.Cleanup(func() { NewScriptPath, WriteScript, removeScript = oldP, oldW, oldR })
	return written, removed
}

// TestSpawnTabLongCommandUsesScript — commands over maxTypedLine (the
// ~1.5KB claude bootstrap prompt) go through a sourced temp script:
// the tab gets a short ` . '<script>'` line, and the script deletes
// itself before running the command with its env prefix intact.
func TestSpawnTabLongCommandUsesScript(t *testing.T) {
	t.Setenv("REX_SESSION", "session:s1")
	written, removed := stubScripts(t)
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte(windowNewJSON), nil })
	var line string
	stubRunner(t, func(args []string) error { line = args[len(args)-1]; return nil })

	long := "claude --session-id x '" + strings.Repeat("p", 1500) + "'"
	env := map[string]string{"FLOW_ROOT": "/r"}
	if err := SpawnTab("t", "/w", long, env); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("scripts written = %d; want 1", len(written))
	}
	for path, body := range written {
		if want := " . '" + path + "'\n"; line != want {
			t.Errorf("typed line = %q; want %q", line, want)
		}
		want := "rm -f -- '" + path + "'\nFLOW_ROOT='/r' " + long + "\n"
		if body != want {
			t.Errorf("script body = %q; want %q", body, want)
		}
	}
	if len(line) > maxTypedLine {
		t.Errorf("typed line is %d bytes; must stay under %d", len(line), maxTypedLine)
	}
	if len(*removed) != 0 {
		t.Errorf("happy path must leave deletion to the script; removed=%v", *removed)
	}
}

// TestSpawnTabShortCommandTypedDirectly — short commands (resumes) are
// still typed as-is, so the tab shows what ran.
func TestSpawnTabShortCommandTypedDirectly(t *testing.T) {
	t.Setenv("REX_SESSION", "session:s1")
	written, _ := stubScripts(t)
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte(windowNewJSON), nil })
	var line string
	stubRunner(t, func(args []string) error { line = args[len(args)-1]; return nil })
	if err := SpawnTab("t", "/w", "claude --resume x", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if len(written) != 0 || line != " claude --resume x\n" {
		t.Errorf("written=%v line=%q; want direct typing", written, line)
	}
}

// TestSpawnTabScriptRemovedWhenSendFails — a script nobody will source
// is cleaned up.
func TestSpawnTabScriptRemovedWhenSendFails(t *testing.T) {
	t.Setenv("REX_SESSION", "session:s1")
	written, removed := stubScripts(t)
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte(windowNewJSON), nil })
	stubRunner(t, func(args []string) error { return errors.New("send failed") })
	if err := SpawnTab("t", "/w", strings.Repeat("x", 600), nil); err == nil {
		t.Fatal("want send error")
	}
	if len(written) != 1 || len(*removed) != 1 {
		t.Errorf("written=%d removed=%v; want the one script removed", len(written), *removed)
	}
}

// TestSpawnScriptRunsInShell executes a real generated script the way
// the tab would (sourced by sh): it deletes itself and runs the command
// with the env prefix applied.
func TestSpawnScriptRunsInShell(t *testing.T) {
	t.Setenv("REX_SESSION", "session:s1")
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	oldP := NewScriptPath
	NewScriptPath = func() (string, error) { return filepath.Join(dir, "s.sh"), nil }
	t.Cleanup(func() { NewScriptPath = oldP })
	stubRunnerOutput(t, func(args []string) ([]byte, error) { return []byte(windowNewJSON), nil })
	var line string
	stubRunner(t, func(args []string) error { line = args[len(args)-1]; return nil })

	// The env prefix must reach the command's process, as FLOW_ROOT
	// must reach claude: a child shell reads it from its environment.
	cmd := `sh -c 'printf "%s" "$FLOW_ROOT ` + strings.Repeat("y", 600) + `"' > ` + ShellQuote(out)
	if err := SpawnTab("t", "/w", cmd, map[string]string{"FLOW_ROOT": "it's here"}); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if r, err := exec.Command("sh", "-c", line).CombinedOutput(); err != nil {
		t.Fatalf("sourcing %q: %v: %s", line, err, r)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := "it's here " + strings.Repeat("y", 600); string(got) != want {
		t.Errorf("command output = %q; want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "s.sh")); !os.IsNotExist(err) {
		t.Errorf("script must delete itself; stat err=%v", err)
	}
}
