package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"flow/internal/connector"
)

// B8: the sample macOS connector (examples/connectors/macos). Real dialogs
// can't pop in tests, so `osascript` on PATH is a fake: JXA calls (`-l
// JavaScript`, the JSON parsing) go to the real /usr/bin/osascript, and the
// AppleScript dialog call records its arguments and prints a canned answer.
// FLOW_BIN is a fake that records how the connector called flow back.

const macosSample = "../../examples/connectors/macos"

type macosHarness struct {
	t                         *testing.T
	env                       []string
	argsFile, callsFile, done string
}

func newMacosHarness(t *testing.T, dialogResult string) *macosHarness {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("macOS sample connector needs osascript")
	}
	if _, err := os.Stat("/usr/bin/osascript"); err != nil {
		t.Skip("no /usr/bin/osascript")
	}
	t.Parallel() // each harness is self-contained (own dirs, explicit env)
	dir := t.TempDir()
	h := &macosHarness{t: t,
		argsFile:  filepath.Join(dir, "dialog-args"),
		callsFile: filepath.Join(dir, "flow-calls"),
		done:      filepath.Join(dir, "dialog-done"),
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeOsa := `#!/bin/bash
if [ "$1" = "-l" ]; then exec /usr/bin/osascript "$@"; fi
printf '%s\0' "$@" > "$FAKE_DIALOG_ARGS"
sleep 1.5
printf '%s\n' "$FAKE_DIALOG_RESULT"
touch "$FAKE_DIALOG_DONE"
`
	fakeFlow := "#!/bin/bash\nprintf '%s\\0' \"$@\" > \"$FAKE_FLOW_CALLS\"\n"
	for name, body := range map[string]string{"osascript": fakeOsa, "flow": fakeFlow} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.env = []string{
		"PATH=" + bin + ":/usr/bin:/bin",
		"HOME=" + dir,
		"TMPDIR=" + dir,
		"FLOW_BIN=" + filepath.Join(bin, "flow"),
		"FLOW_CONNECTOR_TOKEN=tok",
		"FAKE_DIALOG_ARGS=" + h.argsFile,
		"FAKE_DIALOG_RESULT=" + dialogResult,
		"FAKE_DIALOG_DONE=" + h.done,
		"FAKE_FLOW_CALLS=" + h.callsFile,
	}
	return h
}

// run invokes the sample with verb and payload and asserts it returns fast
// (the dialog is detached; the fake dialog alone takes 1.5s).
func (h *macosHarness) run(verb, payload string) {
	h.t.Helper()
	cmd := exec.Command(filepath.Join(macosSample, "run"), verb)
	cmd.Env = h.env
	cmd.Stdin = strings.NewReader(payload)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	if err := cmd.Run(); err != nil {
		h.t.Fatalf("run %s: %v\n%s", verb, err, stderr.String())
	}
	if d := time.Since(start); d > 1400*time.Millisecond {
		h.t.Errorf("run %s took %v — it must fork the dialog and exit", verb, d)
	}
}

func waitFile(t *testing.T, p string) bool {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(p); err == nil {
			time.Sleep(50 * time.Millisecond) // let the writer finish
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func nulFields(t *testing.T, p string) []string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}

// flowCall waits for the fake FLOW_BIN to be called and returns its argv.
func (h *macosHarness) flowCall() []string {
	h.t.Helper()
	if !waitFile(h.t, h.callsFile) {
		h.t.Fatalf("connector never called FLOW_BIN")
	}
	return nulFields(h.t, h.callsFile)
}

// noFlowCall waits for the dialog to finish and asserts flow was not called.
func (h *macosHarness) noFlowCall() {
	h.t.Helper()
	if !waitFile(h.t, h.done) {
		h.t.Fatalf("dialog never ran")
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(h.callsFile); err == nil {
		h.t.Errorf("FLOW_BIN called: %q", nulFields(h.t, h.callsFile))
	}
}

func (h *macosHarness) dialogArgs() []string {
	h.t.Helper()
	if !waitFile(h.t, h.argsFile) {
		h.t.Fatalf("dialog never shown")
	}
	return nulFields(h.t, h.argsFile)
}

func eqArgs(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

const us = "\x1f" // the dialog script's choice/note separator

func TestB8MacosSampleManifestIsValid(t *testing.T) {
	m, err := connector.Load(macosSample)
	if err != nil {
		t.Fatalf("sample manifest invalid: %v", err)
	}
	if m.Name != "macos" || !m.Wants("ask") || !m.Wants("urgent") {
		t.Errorf("manifest = %+v", m)
	}
}

// The dialog AppleScript must compile (osacompile checks it without
// running it, so no dialog appears).
func TestB8MacosDialogScriptCompiles(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("needs osacompile")
	}
	src, err := os.ReadFile(filepath.Join(macosSample, "run"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	i := strings.Index(s, "DIALOG_AS='")
	j := strings.Index(s, "\nend run'")
	if i < 0 || j < i {
		t.Fatal("DIALOG_AS not found in run")
	}
	script := s[i+len("DIALOG_AS='") : j+len("\nend run")]
	out := filepath.Join(t.TempDir(), "dialog.scpt")
	if b, err := exec.Command("osacompile", "-e", script, "-o", out).CombinedOutput(); err != nil {
		t.Fatalf("dialog AppleScript does not compile: %v\n%s", err, b)
	}
}

func TestB8MacosSendChoiceAndNote(t *testing.T) {
	h := newMacosHarness(t, "Yes"+us+"after lunch")
	h.run("send", `{"id":"ab12","from_task":"asker","body":"ship \"v2\"?\nsecond line — ok","options":["Yes","No"],"urgent":false,"kind":"ask","created_at":"2026-11-06T10:00:00Z","reply_hint":"x"}`)
	eqArgs(t, "flow call", h.flowCall(), []string{"human", "reply", "ab12", "Yes — after lunch"})
	args := h.dialogArgs()
	// osascript -e <script> <title> <body> <mode> <later-label> <options...>
	if len(args) < 3 || args[0] != "-e" {
		t.Fatalf("dialog args = %q", args)
	}
	if strings.Contains(args[1], "System Events") || strings.Contains(args[1], "giving up after") || !strings.Contains(args[1], "with timeout") {
		t.Errorf("dialog script must use plain display dialog, never auto-dismiss, and wrap a long timeout")
	}
	eqArgs(t, "dialog argv", args[2:], []string{"flow · asker", "ship \"v2\"?\nsecond line — ok", "ask", "Later", "Yes", "No"})
}

func TestB8MacosSendChoiceWithoutNote(t *testing.T) {
	h := newMacosHarness(t, "No"+us)
	h.run("send", `{"id":"ab13","from_task":"asker","body":"q","options":["Yes","No"],"urgent":true,"kind":"ask"}`)
	eqArgs(t, "flow call", h.flowCall(), []string{"human", "reply", "ab13", "No"})
	args := h.dialogArgs()
	if args[3] != "URGENT: q" {
		t.Errorf("urgent body = %q", args[3])
	}
}

func TestB8MacosSendNoOptionsReplyIsTheNote(t *testing.T) {
	h := newMacosHarness(t, "Reply"+us+"sounds good")
	h.run("send", `{"id":"ab14","from_task":"","body":"thoughts?","options":[],"urgent":false,"kind":"ask"}`)
	eqArgs(t, "flow call", h.flowCall(), []string{"human", "reply", "ab14", "sounds good"})
	eqArgs(t, "dialog argv", h.dialogArgs()[2:], []string{"flow", "thoughts?", "ask", "Later"})
}

func TestB8MacosSendEmptyReplyMarksSeen(t *testing.T) {
	h := newMacosHarness(t, "Reply"+us)
	h.run("send", `{"id":"ab15","from_task":"t","body":"fyi-ish","options":[],"kind":"ask"}`)
	eqArgs(t, "flow call", h.flowCall(), []string{"human", "seen", "ab15"})
}

func TestB8MacosSendManyOptions(t *testing.T) {
	h := newMacosHarness(t, "B"+us+"because")
	h.run("send", `{"id":"ab16","from_task":"t","body":"pick","options":["A","B","Later"],"kind":"ask"}`)
	eqArgs(t, "flow call", h.flowCall(), []string{"human", "reply", "ab16", "B — because"})
	// An option literally named "Later" moves the dismiss button to "Not now".
	eqArgs(t, "dialog argv", h.dialogArgs()[2:], []string{"flow · t", "pick", "ask", "Not now", "A", "B", "Later"})
}

func TestB8MacosLaterDoesNothing(t *testing.T) {
	h := newMacosHarness(t, "LATER")
	h.run("send", `{"id":"ab17","from_task":"t","body":"q","options":["Yes","No"],"kind":"ask"}`)
	h.noFlowCall()
}

func TestB8MacosTestVerbShowsOKDialogOnly(t *testing.T) {
	h := newMacosHarness(t, "OK")
	h.run("test", `{"id":"test","from_task":"flow","body":"flow connector test","options":[],"kind":"test"}`)
	h.noFlowCall()
	eqArgs(t, "dialog argv", h.dialogArgs()[2:], []string{"flow · test", "flow connector test", "test", "Later"})
}

func TestB8MacosRejectsBadInput(t *testing.T) {
	h := newMacosHarness(t, "LATER")
	for verb, payload := range map[string]string{"send": "not json", "bogus": "{}"} {
		cmd := exec.Command(filepath.Join(macosSample, "run"), verb)
		cmd.Env = h.env
		cmd.Stdin = strings.NewReader(payload)
		if err := cmd.Run(); err == nil {
			t.Errorf("%s %q: exit 0", verb, payload)
		}
	}
}
