package claude

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"flow/internal/harness"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestRecordWakeEndpointWrites0600(t *testing.T) {
	dir := t.TempDir()
	env := envMap(map[string]string{
		"CLAUDE_CODE_MESSAGING_SOCKET": "/tmp/cc-socks/1.sock",
		"CLAUDE_CODE_MESSAGING_TOKEN":  "tok-1",
		"CLAUDE_PID":                   "4242",
	})
	if err := RecordWakeEndpoint(dir, "sid-1", env); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "claude", "sid-1.json")
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600 (the token authenticates socket sends)", fi.Mode().Perm())
	}
	if di, _ := os.Stat(filepath.Dir(p)); di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v, want 0700", di.Mode().Perm())
	}
	ep, err := readWakeEndpoint(dir, "sid-1")
	if err != nil || ep.Socket != "/tmp/cc-socks/1.sock" || ep.Token != "tok-1" || ep.PID != 4242 {
		t.Fatalf("endpoint = %+v, %v", ep, err)
	}
	// Respawn (resume / compact): overwritten with the new pid + token.
	env2 := envMap(map[string]string{
		"CLAUDE_CODE_MESSAGING_SOCKET": "/tmp/cc-socks/2.sock",
		"CLAUDE_CODE_MESSAGING_TOKEN":  "tok-2",
		"CLAUDE_PID":                   "4343",
	})
	if err := RecordWakeEndpoint(dir, "sid-1", env2); err != nil {
		t.Fatal(err)
	}
	if ep, _ := readWakeEndpoint(dir, "sid-1"); ep.Token != "tok-2" || ep.PID != 4343 {
		t.Fatalf("not overwritten: %+v", ep)
	}
}

func TestRecordWakeEndpointNoSocketRemovesStale(t *testing.T) {
	dir := t.TempDir()
	env := envMap(map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": "/s", "CLAUDE_CODE_MESSAGING_TOKEN": "t", "CLAUDE_PID": "1"})
	_ = RecordWakeEndpoint(dir, "sid-1", env)
	// An older Claude (or a refused socket) starts the session without the
	// env: the stale record must go, or wake would dial a dead socket.
	if err := RecordWakeEndpoint(dir, "sid-1", envMap(nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "claude", "sid-1.json")); !os.IsNotExist(err) {
		t.Fatalf("stale endpoint kept: %v", err)
	}
}

func TestWakeNoEndpointUnavailable(t *testing.T) {
	_, err := New().(harness.Waker).Wake(harness.WakeRequest{SessionID: "nope", Notice: "x", StateDir: t.TempDir(), Timeout: time.Second})
	if !errors.Is(err, harness.ErrWakeUnavailable) {
		t.Fatalf("err = %v, want ErrWakeUnavailable", err)
	}
}

func TestWakeDeadPidUnavailable(t *testing.T) {
	dir := t.TempDir()
	env := envMap(map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": "/s", "CLAUDE_CODE_MESSAGING_TOKEN": "t", "CLAUDE_PID": "999999"})
	_ = RecordWakeEndpoint(dir, "sid-d", env)
	orig := ProcessAlive
	t.Cleanup(func() { ProcessAlive = orig })
	ProcessAlive = func(int) bool { return false }
	_, err := New().(harness.Waker).Wake(harness.WakeRequest{SessionID: "sid-d", Notice: "x", StateDir: dir, Timeout: time.Second})
	if !errors.Is(err, harness.ErrWakeUnavailable) {
		t.Fatalf("err = %v, want ErrWakeUnavailable", err)
	}
}

// The verified wire protocol: one connection, newline-terminated JSON
// lines — auth with the session's token, then a user message.
func TestWakeSendsAuthThenUserLine(t *testing.T) {
	sockDir, err := os.MkdirTemp("/tmp", "cw") // unix socket paths are length-capped
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		var lines []string
		sc := bufio.NewScanner(c)
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		got <- lines
	}()

	dir := t.TempDir()
	env := envMap(map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": sock, "CLAUDE_CODE_MESSAGING_TOKEN": "tok-x",
		"CLAUDE_PID": strconv.Itoa(os.Getpid())})
	if err := RecordWakeEndpoint(dir, "sid-s", env); err != nil {
		t.Fatal(err)
	}
	if _, err := New().(harness.Waker).Wake(harness.WakeRequest{SessionID: "sid-s", Notice: "[flow-bus wake] hi",
		StateDir: dir, Timeout: 2 * time.Second}); err != nil {
		t.Fatalf("wake: %v", err)
	}
	var lines []string
	select {
	case lines = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("socket got nothing")
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %q, want auth + user", lines)
	}
	var auth struct{ Type, Token string }
	_ = json.Unmarshal([]byte(lines[0]), &auth)
	if auth.Type != "auth" || auth.Token != "tok-x" {
		t.Errorf("auth line = %s", lines[0])
	}
	var user struct {
		Type    string
		Message struct{ Role, Content string }
	}
	_ = json.Unmarshal([]byte(lines[1]), &user)
	if user.Type != "user" || user.Message.Role != "user" || user.Message.Content != "[flow-bus wake] hi" {
		t.Errorf("user line = %s", lines[1])
	}
}

func TestWakeConnectRefusedIsError(t *testing.T) {
	dir := t.TempDir()
	env := envMap(map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": filepath.Join(dir, "gone.sock"),
		"CLAUDE_CODE_MESSAGING_TOKEN": "t", "CLAUDE_PID": strconv.Itoa(os.Getpid())})
	_ = RecordWakeEndpoint(dir, "sid-g", env)
	if _, err := New().(harness.Waker).Wake(harness.WakeRequest{SessionID: "sid-g", Notice: "x", StateDir: dir, Timeout: time.Second}); err == nil {
		t.Fatal("dial of a missing socket must fail so the fallback can claim the rows")
	}
}

func readHookEntries(t *testing.T, home, event string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Hooks map[string][]map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s.Hooks[event]
}

func TestInstallRewakeHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rw := New().(harness.RewakeHookInstaller)
	if rw.RewakeHookInstalled("flow hook wake-wait") {
		t.Fatal("reported installed before install")
	}
	added, err := rw.InstallRewakeHook("flow hook wake-wait", "flow bus:")
	if err != nil || !added {
		t.Fatalf("install added=%v err=%v", added, err)
	}
	for _, ev := range []string{"SessionStart", "Stop"} {
		e := readHookEntries(t, home, ev)
		if len(e) != 1 {
			t.Fatalf("%s entries = %v", ev, e)
		}
		inner := e[0]["hooks"].([]any)[0].(map[string]any)
		if inner["command"] != "flow hook wake-wait" || inner["asyncRewake"] != true ||
			inner["timeout"] != float64(86400) || inner["rewakeMessage"] != "flow bus:" {
			t.Errorf("%s hook = %v", ev, inner)
		}
		if ev == "SessionStart" && e[0]["matcher"] != hookMatcher {
			t.Errorf("SessionStart matcher = %v", e[0]["matcher"])
		}
	}
	if !rw.RewakeHookInstalled("flow hook wake-wait") {
		t.Error("not reported installed")
	}
	if added, err := rw.InstallRewakeHook("flow hook wake-wait", "flow bus:"); err != nil || added {
		t.Fatalf("re-install added=%v err=%v, want idempotent", added, err)
	}
	removed, err := rw.UninstallRewakeHook("flow hook wake-wait")
	if err != nil || !removed {
		t.Fatalf("uninstall removed=%v err=%v", removed, err)
	}
	if rw.RewakeHookInstalled("flow hook wake-wait") {
		t.Error("still installed after uninstall")
	}
}

// An entry for the command that lost its asyncRewake fields (hand edit,
// older install) is repaired in place, not duplicated.
func TestInstallRewakeHookRepairsFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	old := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"flow hook wake-wait"}]}]}}`
	_ = os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(old), 0o644)
	rw := New().(harness.RewakeHookInstaller)
	if added, err := rw.InstallRewakeHook("flow hook wake-wait", "m"); err != nil || !added {
		t.Fatalf("repair added=%v err=%v", added, err)
	}
	e := readHookEntries(t, home, "Stop")
	if len(e) != 1 {
		t.Fatalf("duplicated: %v", e)
	}
	if e[0]["hooks"].([]any)[0].(map[string]any)["asyncRewake"] != true {
		t.Errorf("not repaired: %v", e)
	}
}

// Under crossSessionInbound hold/refuse a socket push is held, not
// delivered: flow must not record the endpoint (the waiter takes over).
func TestRecordWakeEndpointSkipsHeldInbound(t *testing.T) {
	for _, pol := range []string{"hold", "refuse"} {
		dir := t.TempDir()
		orig := InboundPolicy
		InboundPolicy = func() string { return pol }
		env := envMap(map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": "/s", "CLAUDE_CODE_MESSAGING_TOKEN": "t", "CLAUDE_PID": "1"})
		if err := RecordWakeEndpoint(dir, "sid", env); err != nil {
			t.Fatal(err)
		}
		InboundPolicy = orig
		if _, err := os.Stat(filepath.Join(dir, "claude", "sid.json")); !os.IsNotExist(err) {
			t.Fatalf("%s: endpoint recorded", pol)
		}
	}
}

func TestSessionBusyCrossCheck(t *testing.T) {
	orig := BGCommandRunner
	t.Cleanup(func() { BGCommandRunner = orig })
	BGCommandRunner = func(_ string, args []string) ([]byte, error) {
		return []byte(`[{"pid":1,"sessionId":"aaa","kind":"interactive","status":"idle"},
			{"pid":2,"sessionId":"bbb","kind":"background","status":"busy"}]`), nil
	}
	c := New().(harness.TurnStateChecker)
	if busy, known := c.SessionBusy("aaa", time.Second); busy || !known {
		t.Errorf("aaa = %v %v, want idle known", busy, known)
	}
	if busy, known := c.SessionBusy("bbb", time.Second); !busy || !known {
		t.Errorf("bbb = %v %v, want busy known", busy, known)
	}
	if _, known := c.SessionBusy("zzz", time.Second); known {
		t.Error("absent session must be unknown")
	}
	BGCommandRunner = func(string, []string) ([]byte, error) { time.Sleep(2 * time.Second); return nil, nil }
	if _, known := c.SessionBusy("aaa", 50*time.Millisecond); known {
		t.Error("a slow query must time out as unknown")
	}
}
