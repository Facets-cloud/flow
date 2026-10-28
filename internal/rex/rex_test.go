package rex

import (
	"errors"
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

func TestFocusSessionOutsideRex(t *testing.T) {
	t.Setenv("REX_SESSION", "")
	stubRunnerOutput(t, func(args []string) ([]byte, error) {
		t.Errorf("no rex calls expected outside a Rex session: %v", args)
		return nil, nil
	})
	ok, err := FocusSession(uuidA, "claude")
	if err != nil || ok {
		t.Fatalf("FocusSession = (%v, %v); want (false, nil)", ok, err)
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
