package app

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/harness/claude"
	"flow/internal/rex"
	"flow/internal/spawner"
)

func TestRexNotificationStatus(t *testing.T) {
	cases := []struct {
		payload          string
		state, kind, msg string
		ok               bool
	}{
		{`{"notification_type":"permission_prompt","message":"Claude needs your permission to use Bash"}`,
			"blocked", "permission", "Claude needs your permission to use Bash", true},
		{`{"notification_type":"elicitation_dialog","message":"Pick one"}`, "blocked", "question", "Pick one", true},
		// A finished turn idling for input must not overwrite "done".
		{`{"notification_type":"idle_prompt","message":"Claude is waiting for your input"}`, "", "", "", false},
		{`{"notification_type":"auth_success"}`, "", "", "", false},
		{`not json`, "", "", "", false},
		{``, "", "", "", false},
	}
	for _, tc := range cases {
		state, kind, msg, ok := rexNotificationStatus(strings.NewReader(tc.payload))
		if state != tc.state || kind != tc.kind || msg != tc.msg || ok != tc.ok {
			t.Errorf("payload %s → (%q,%q,%q,%v); want (%q,%q,%q,%v)",
				tc.payload, state, kind, msg, ok, tc.state, tc.kind, tc.msg, tc.ok)
		}
	}
}

type nopWriteCloser struct{ strings.Builder }

func (n *nopWriteCloser) Close() error { return nil }

// stubStatusTTY routes ReportStatus writes into memory: the hook's
// parent "owns" a tty.
func stubStatusTTY(t *testing.T) *nopWriteCloser {
	t.Helper()
	w := &nopWriteCloser{}
	oldO, oldP := rex.OpenTTY, rex.ProcTTY
	rex.OpenTTY = func(string) (io.WriteCloser, error) { return w, nil }
	rex.ProcTTY = func(int) (int, string, error) { return 1, "ttys001", nil }
	t.Cleanup(func() { rex.OpenTTY, rex.ProcTTY = oldO, oldP })
	return w
}

// TestCmdHookRexStatusNeverPrints — UserPromptSubmit stdout becomes model
// context, so the hook must stay silent and exit 0 for every input,
// valid or not, inside or outside Rex.
func TestCmdHookRexStatusNeverPrints(t *testing.T) {
	stubStatusTTY(t)
	for _, rexSession := range []string{"", "session:s1"} {
		t.Setenv("REX_SESSION", rexSession)
		for _, args := range [][]string{{"working"}, {"done"}, {"bogus"}, {}, {"working", "extra"}} {
			var rc int
			out := captureStdout(t, func() { rc = cmdHook(append([]string{"rex-status"}, args...)) })
			if rc != 0 || out != "" {
				t.Errorf("REX_SESSION=%q args=%v: rc=%d stdout=%q; want 0 and silent", rexSession, args, rc, out)
			}
		}
	}
}

func TestCmdHookRexStatusReports(t *testing.T) {
	t.Setenv("REX_SESSION", "session:s1")
	w := stubStatusTTY(t)
	cmdHookRexStatus([]string{"working"})
	if !strings.Contains(w.String(), "state=working:app=claude-code") {
		t.Errorf("working wrote %q", w.String())
	}
}

func TestCmdHookRexStatusNotificationFromStdin(t *testing.T) {
	t.Setenv("REX_SESSION", "session:s1")
	w := stubStatusTTY(t)
	feedStdin(t, `{"notification_type":"permission_prompt","message":"Allow Bash?"}`)

	if rc := cmdHookRexStatus([]string{"notification"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !strings.Contains(w.String(), "state=blocked:kind=permission:app=claude-code:msg=") {
		t.Errorf("notification wrote %q", w.String())
	}
}

// TestRexStatusHooksInstallInsideRex — inside Rex, install registers
// all five event hooks (Notification with its matcher), is idempotent,
// and uninstall removes exactly them, leaving flow's other hooks.
func TestRexStatusHooksInstallInsideRex(t *testing.T) {
	home := withTempHome(t)
	old := spawner.Override
	spawner.Override = spawner.BackendRex
	t.Cleanup(func() { spawner.Override = old })
	h := claude.New()
	if _, err := h.InstallStopHook(stopHookCommand); err != nil {
		t.Fatal(err)
	}

	n, err := installRexStatusHooks(h)
	if err != nil || n != len(rexStatusHooks) {
		t.Fatalf("install = (%d, %v); want (%d, nil)", n, err, len(rexStatusHooks))
	}
	settings := readSettings(t, filepath.Join(home, ".claude", "settings.json"))
	hooks, _ := settings["hooks"].(map[string]any)
	for _, hk := range rexStatusHooks {
		if !hookEventReferencesCommand(hooks, hk.event, hk.command) {
			t.Errorf("%s missing %q", hk.event, hk.command)
		}
	}
	matcher := ""
	for _, e := range hooks["Notification"].([]any) {
		if m, _ := e.(map[string]any)["matcher"].(string); m != "" {
			matcher = m
		}
	}
	if matcher != "permission_prompt|elicitation_dialog" {
		t.Errorf("Notification matcher = %q", matcher)
	}

	if n, _ := installRexStatusHooks(h); n != 0 {
		t.Errorf("second install added %d; want 0 (idempotent)", n)
	}

	if n, err := uninstallRexStatusHooks(h); err != nil || n != len(rexStatusHooks) {
		t.Fatalf("uninstall = (%d, %v); want (%d, nil)", n, err, len(rexStatusHooks))
	}
	settings = readSettings(t, filepath.Join(home, ".claude", "settings.json"))
	hooks, _ = settings["hooks"].(map[string]any)
	for _, hk := range rexStatusHooks {
		if hookEventReferencesCommand(hooks, hk.event, hk.command) {
			t.Errorf("%s still has %q after uninstall", hk.event, hk.command)
		}
	}
	if !hookEventReferencesCommand(hooks, "Stop", stopHookCommand) {
		t.Error("uninstall removed flow's own Stop hook")
	}
}

// TestRexStatusHooksSkippedOutsideRex — non-Rex users never get the
// extra per-event hook process.
func TestRexStatusHooksSkippedOutsideRex(t *testing.T) {
	home := withTempHome(t)
	old := spawner.Override
	spawner.Override = spawner.BackendKitty
	t.Cleanup(func() { spawner.Override = old })
	if n, err := installRexStatusHooks(claude.New()); err != nil || n != 0 {
		t.Fatalf("install outside Rex = (%d, %v); want (0, nil)", n, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Error("settings.json must not be touched outside Rex")
	}
}

func feedStdin(t *testing.T, payload string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
	io.WriteString(w, payload)
	w.Close()
}
