package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetTmuxConfigOnce flips the per-process write guard back to "not
// written yet" so consecutive tests within the same `go test` invocation
// each get a fresh write path. Without this, the second test sees
// tmuxConfigWritten=true from the first and short-circuits before
// creating a file. Production doesn't need this — a single flow server
// process only ever writes once.
func resetTmuxConfigOnce(t *testing.T) {
	t.Helper()
	tmuxConfigWriteOnce.Lock()
	tmuxConfigWritten = false
	tmuxConfigWriteOnce.Unlock()
}

func TestEnsureTmuxConfigWritesFileWhenAbsent(t *testing.T) {
	resetTmuxConfigOnce(t)
	dir := t.TempDir()
	path, err := ensureTmuxConfig(dir)
	if err != nil {
		t.Fatalf("ensureTmuxConfig: %v", err)
	}
	wantPath := filepath.Join(dir, "tmux.conf")
	if path != wantPath {
		t.Errorf("path = %q, want %q", path, wantPath)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"set -g mouse on",
		"set -g set-clipboard on",
		"set -g history-limit 2147483647",
		"bind-key -T copy-mode    MouseDragEnd1Pane send-keys -X copy-pipe-and-cancel",
		"bind-key -T copy-mode-vi MouseDragEnd1Pane send-keys -X copy-pipe-and-cancel",
		"~/.tmux.conf",
	} {
		if !strings.Contains(string(contents), want) {
			t.Errorf("tmux.conf missing %q; contents=%s", want, contents)
		}
	}
}

func TestEnsureTmuxConfigPreservesUserEdits(t *testing.T) {
	resetTmuxConfigOnce(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "tmux.conf")
	custom := "# user-edited\nset -g status-position top\n"
	if err := os.WriteFile(path, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ensureTmuxConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Errorf("path = %q, want %q", got, path)
	}
	// Critical contract: a pre-existing file is NEVER overwritten so
	// users can hand-edit flow's defaults without losing them on restart.
	contents, _ := os.ReadFile(path)
	if string(contents) != custom {
		t.Errorf("user content overwritten\n got: %q\nwant: %q", contents, custom)
	}
}

func TestEnsureTmuxConfigIsIdempotentInProcess(t *testing.T) {
	resetTmuxConfigOnce(t)
	dir := t.TempDir()
	// First call writes; record mtime.
	if _, err := ensureTmuxConfig(dir); err != nil {
		t.Fatal(err)
	}
	stat1, err := os.Stat(filepath.Join(dir, "tmux.conf"))
	if err != nil {
		t.Fatal(err)
	}
	// Second call must short-circuit (sync.Mutex + tmuxConfigWritten
	// flag) — the file should not be touched again.
	if _, err := ensureTmuxConfig(dir); err != nil {
		t.Fatal(err)
	}
	stat2, err := os.Stat(filepath.Join(dir, "tmux.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !stat1.ModTime().Equal(stat2.ModTime()) {
		t.Errorf("file was re-written on second call; mtime changed %v → %v",
			stat1.ModTime(), stat2.ModTime())
	}
}

func TestEnsureTmuxConfigRejectsEmptyFlowRoot(t *testing.T) {
	resetTmuxConfigOnce(t)
	_, err := ensureTmuxConfig("")
	if err == nil {
		t.Errorf("expected error for empty flow root, got nil")
	}
}

func TestEnsureSharedTerminalScrollOptionsAppliesPerSession(t *testing.T) {
	oldSharedCommand := sharedTerminalCommand
	defer func() { sharedTerminalCommand = oldSharedCommand }()

	var commands [][]string
	sharedTerminalCommand = func(args ...string) ([]byte, error) {
		commands = append(commands, append([]string(nil), args...))
		return nil, nil
	}

	if err := ensureSharedTerminalScrollOptions("flow-build-ui"); err != nil {
		t.Fatalf("ensureSharedTerminalScrollOptions: %v", err)
	}

	got := strings.TrimSpace(commandLog(commands))
	for _, want := range []string{
		"set-option -t flow-build-ui mouse on",
		"set-option -t flow-build-ui set-clipboard on",
		"set-window-option -t flow-build-ui: history-limit 2147483647",
		"bind-key -T copy-mode MouseDragEnd1Pane send-keys -X copy-pipe-and-cancel",
		"bind-key -T copy-mode-vi MouseDragEnd1Pane send-keys -X copy-pipe-and-cancel",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing tmux command %q in:\n%s", want, got)
		}
	}
}

func TestEnsureSharedTerminalDefaultScrollOptionsAppliesBeforeNewWindows(t *testing.T) {
	oldSharedCommand := sharedTerminalCommand
	defer func() { sharedTerminalCommand = oldSharedCommand }()

	var commands [][]string
	sharedTerminalCommand = func(args ...string) ([]byte, error) {
		commands = append(commands, append([]string(nil), args...))
		return nil, nil
	}

	if err := ensureSharedTerminalDefaultScrollOptions(); err != nil {
		t.Fatalf("ensureSharedTerminalDefaultScrollOptions: %v", err)
	}

	got := strings.TrimSpace(commandLog(commands))
	for _, want := range []string{
		"set-option -g mouse on",
		"set-option -g set-clipboard on",
		"set-window-option -g history-limit 2147483647",
		"bind-key -T copy-mode MouseDragEnd1Pane send-keys -X copy-pipe-and-cancel",
		"bind-key -T copy-mode-vi MouseDragEnd1Pane send-keys -X copy-pipe-and-cancel",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing tmux command %q in:\n%s", want, got)
		}
	}
}

func TestEnsureSharedTerminalSessionSetsMaxHistoryBeforeNewWindow(t *testing.T) {
	oldSharedLookPath := sharedTerminalLookPath
	oldSharedCommand := sharedTerminalCommand
	sharedTerminalLookPath = func(name string) (string, error) {
		if name == "tmux" {
			return "/usr/bin/tmux", nil
		}
		return "", fmt.Errorf("not found")
	}
	resetSharedTerminalAvailable()
	defer func() {
		sharedTerminalLookPath = oldSharedLookPath
		sharedTerminalCommand = oldSharedCommand
		resetSharedTerminalAvailable()
	}()

	var commands [][]string
	sessionExists := false
	sharedTerminalCommand = func(args ...string) ([]byte, error) {
		commands = append(commands, append([]string(nil), args...))
		if len(args) > 0 && args[0] == "has-session" {
			if sessionExists {
				return nil, nil
			}
			return nil, fmt.Errorf("missing session")
		}
		if containsString(args, "new-session") {
			sessionExists = true
		}
		return nil, nil
	}

	srv := &Server{cfg: Config{FlowRoot: t.TempDir()}}
	if _, created, err := srv.ensureSharedTerminalSession(terminalLaunch{
		Slug:     "build-ui",
		Provider: "claude",
		WorkDir:  t.TempDir(),
		Args:     []string{"--resume", "session-id"},
	}, 120, 32); err != nil {
		t.Fatalf("ensureSharedTerminalSession: %v", err)
	} else if !created {
		t.Fatal("expected a new tmux session")
	}

	got := strings.TrimSpace(commandLog(commands))
	want := "set-option -g mouse on ; set-option -g status off ; set-option -g set-clipboard on ; " +
		"bind-key -T copy-mode MouseDragEnd1Pane send-keys -X copy-pipe-and-cancel ; " +
		"bind-key -T copy-mode-vi MouseDragEnd1Pane send-keys -X copy-pipe-and-cancel ; " +
		"set-window-option -g history-limit 2147483647 ; new-session"
	if !strings.Contains(got, want) {
		t.Fatalf("tmux creation command must apply mouse + status-off + OSC 52 clipboard + copy bindings + max history before new-session; missing %q in:\n%s", want, got)
	}
}

func TestEnsureSharedTerminalSessionReplacesProviderMismatch(t *testing.T) {
	oldSharedLookPath := sharedTerminalLookPath
	oldSharedCommand := sharedTerminalCommand
	sharedTerminalLookPath = func(name string) (string, error) {
		if name == "tmux" {
			return "/usr/bin/tmux", nil
		}
		return "", fmt.Errorf("not found")
	}
	resetSharedTerminalAvailable()
	defer func() {
		sharedTerminalLookPath = oldSharedLookPath
		sharedTerminalCommand = oldSharedCommand
		resetSharedTerminalAvailable()
	}()

	var commands [][]string
	sessionExists := true
	sharedTerminalCommand = func(args ...string) ([]byte, error) {
		commands = append(commands, append([]string(nil), args...))
		if len(args) == 0 {
			return nil, nil
		}
		switch args[0] {
		case "has-session":
			if sessionExists {
				return nil, nil
			}
			return nil, fmt.Errorf("missing session")
		case "list-panes":
			return []byte("FLOW_SESSION_PROVIDER='claude' FLOW_TASK='build-ui' claude --resume old\n"), nil
		case "kill-session":
			sessionExists = false
			return nil, nil
		default:
			if containsString(args, "new-session") {
				sessionExists = true
			}
			return nil, nil
		}
	}

	srv := &Server{cfg: Config{FlowRoot: t.TempDir()}}
	if _, created, err := srv.ensureSharedTerminalSession(terminalLaunch{
		Slug:     "build-ui",
		Provider: "codex",
		WorkDir:  t.TempDir(),
		Args:     []string{"exec", "prompt"},
	}, 120, 32); err != nil {
		t.Fatalf("ensureSharedTerminalSession: %v", err)
	} else if !created {
		t.Fatal("expected stale claude tmux session to be replaced by a new codex session")
	}

	got := strings.TrimSpace(commandLog(commands))
	for _, want := range []string{
		"list-panes -t flow-build-ui -F #{pane_start_command}",
		"kill-session -t flow-build-ui",
		"FLOW_SESSION_PROVIDER='codex'",
		"new-session",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in tmux command log:\n%s", want, got)
		}
	}
}

func TestEnsureSharedTerminalSessionReplacesPermissionMismatch(t *testing.T) {
	oldSharedLookPath := sharedTerminalLookPath
	oldSharedCommand := sharedTerminalCommand
	sharedTerminalLookPath = func(name string) (string, error) {
		if name == "tmux" {
			return "/usr/bin/tmux", nil
		}
		return "", fmt.Errorf("not found")
	}
	resetSharedTerminalAvailable()
	defer func() {
		sharedTerminalLookPath = oldSharedLookPath
		sharedTerminalCommand = oldSharedCommand
		resetSharedTerminalAvailable()
	}()

	var commands [][]string
	sessionExists := true
	sharedTerminalCommand = func(args ...string) ([]byte, error) {
		commands = append(commands, append([]string(nil), args...))
		if len(args) == 0 {
			return nil, nil
		}
		switch args[0] {
		case "has-session":
			if sessionExists {
				return nil, nil
			}
			return nil, fmt.Errorf("missing session")
		case "list-panes":
			return []byte("FLOW_PERMISSION_MODE='default' FLOW_SESSION_PROVIDER='codex' FLOW_TASK='build-ui' codex --ask-for-approval on-request --sandbox workspace-write\n"), nil
		case "kill-session":
			sessionExists = false
			return nil, nil
		default:
			if containsString(args, "new-session") {
				sessionExists = true
			}
			return nil, nil
		}
	}

	srv := &Server{cfg: Config{FlowRoot: t.TempDir()}}
	if _, created, err := srv.ensureSharedTerminalSession(terminalLaunch{
		Slug:           "build-ui",
		Provider:       "codex",
		PermissionMode: "bypass",
		WorkDir:        t.TempDir(),
		Args:           []string{"--dangerously-bypass-approvals-and-sandbox", "prompt"},
	}, 120, 32); err != nil {
		t.Fatalf("ensureSharedTerminalSession: %v", err)
	} else if !created {
		t.Fatal("expected stale default-permission tmux session to be replaced by a new bypass session")
	}

	got := strings.TrimSpace(commandLog(commands))
	for _, want := range []string{
		"list-panes -t flow-build-ui -F #{pane_start_command}",
		"kill-session -t flow-build-ui",
		"FLOW_PERMISSION_MODE='bypass'",
		"new-session",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in tmux command log:\n%s", want, got)
		}
	}
}

func TestSharedTerminalCaptureHistoryUsesTmuxHistoryOnlyRange(t *testing.T) {
	oldSharedCommand := sharedTerminalCommand
	defer func() { sharedTerminalCommand = oldSharedCommand }()

	var commands [][]string
	sharedTerminalCommand = func(args ...string) ([]byte, error) {
		commands = append(commands, append([]string(nil), args...))
		return []byte("older line\nnewer line\n"), nil
	}

	got, err := sharedTerminalCaptureHistory("flow-build-ui")
	if err != nil {
		t.Fatalf("sharedTerminalCaptureHistory: %v", err)
	}
	if string(got) != "older line\r\nnewer line\r\n" {
		t.Fatalf("captured history = %q", string(got))
	}

	want := "capture-pane -p -e -S - -E -1 -t flow-build-ui"
	if log := commandLog(commands); !strings.Contains(log, want) {
		t.Fatalf("capture must read tmux history before the visible pane; missing %q in:\n%s", want, log)
	}
}

func commandLog(commands [][]string) string {
	var b strings.Builder
	for _, cmd := range commands {
		b.WriteString(strings.Join(cmd, " "))
		b.WriteByte('\n')
	}
	return b.String()
}
