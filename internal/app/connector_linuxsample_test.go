package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flow/internal/connector"
)

// The sample Linux connector (examples/connectors/linux): a desktop
// dialog via zenity (or kdialog) for asks, notify-send for the test
// notification and for asks when no dialog tool exists, and a clear
// failure (exit 1, "stays in flow inbox") on a box with no display. The
// desktop tools on PATH are fakes that record their argv; FLOW_BIN is a
// fake that records how the connector called flow back. The script is
// plain bash + jq|python3, so these tests run on macOS and Linux alike.

const linuxSample = "../../examples/connectors/linux"

type linuxHarness struct {
	t                                        *testing.T
	env                                      []string
	bin, dialogArgs, notifyArgs, calls, done string
}

type linuxTools struct {
	zenity, kdialog, notify bool
	listResult, entryResult string
	listCancel, entryCancel bool
	noDisplay, wayland      bool
}

func newLinuxHarness(t *testing.T, tools linuxTools) *linuxHarness {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("needs bash")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		if _, err := exec.LookPath("jq"); err != nil {
			t.Skip("needs jq or python3")
		}
	}
	t.Parallel()
	dir := t.TempDir()
	h := &linuxHarness{t: t,
		bin:        filepath.Join(dir, "bin"),
		dialogArgs: filepath.Join(dir, "dialog-args"),
		notifyArgs: filepath.Join(dir, "notify-args"),
		calls:      filepath.Join(dir, "flow-calls"),
		done:       filepath.Join(dir, "dialog-done"),
	}
	if err := os.MkdirAll(h.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	// One fake serves zenity and kdialog: record argv (appending — an ask
	// may show a pick list and then a note box), sleep so a non-detached
	// connector would visibly stall, then answer per the env knobs.
	fakeDialog := `#!/bin/bash
# Like GTK in a non-UTF-8 locale: argv that is not plain ASCII is rejected
# ("Invalid byte sequence in conversion input") unless the locale is UTF-8.
case "${LC_ALL:-${LC_CTYPE:-${LANG:-}}}" in
*[Uu][Tt][Ff]-8*|*[Uu][Tt][Ff]8*) ;;
*) if printf '%s' "$*" | LC_ALL=C grep -q "$(printf '[^ -~\t]')"; then
     echo "zenity: Invalid byte sequence in conversion input" >&2; touch "$FAKE_DIALOG_DONE"; exit 255
   fi ;;
esac
printf '%s\0' "$@" >> "$FAKE_DIALOG_ARGS"
printf -- '--\0' >> "$FAKE_DIALOG_ARGS"
sleep 1.5
case " $* " in
*" --list "*|*" --combobox "*)
  touch "$FAKE_DIALOG_DONE"
  [ "$FAKE_LIST_CANCEL" = 1 ] && exit 1
  printf '%s\n' "$FAKE_LIST_RESULT"; exit 0 ;;
*" --entry "*|*" --inputbox "*)
  touch "$FAKE_DIALOG_DONE"
  [ "$FAKE_ENTRY_CANCEL" = 1 ] && exit 1
  printf '%s\n' "$FAKE_ENTRY_RESULT"; exit 0 ;;
*) touch "$FAKE_DIALOG_DONE"; exit 0 ;;
esac
`
	fakeNotify := "#!/bin/bash\nprintf '%s\\0' \"$@\" >> \"$FAKE_NOTIFY_ARGS\"\ntouch \"$FAKE_DIALOG_DONE\"\n"
	fakeFlow := "#!/bin/bash\nprintf '%s\\0' \"$@\" > \"$FAKE_FLOW_CALLS\"\n"
	fakes := map[string]string{"flow": fakeFlow}
	if tools.zenity {
		fakes["zenity"] = fakeDialog
	}
	if tools.kdialog {
		fakes["kdialog"] = fakeDialog
	}
	if tools.notify {
		fakes["notify-send"] = fakeNotify
	}
	for name, body := range fakes {
		if err := os.WriteFile(filepath.Join(h.bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Real jq/python3 and sh utilities come from the system dirs; the
	// desktop tools only from our fake bin, so "absent" means absent.
	sys := "/usr/bin:/bin"
	if p, err := exec.LookPath("python3"); err == nil {
		sys = filepath.Dir(p) + ":" + sys
	}
	if p, err := exec.LookPath("jq"); err == nil {
		sys = filepath.Dir(p) + ":" + sys
	}
	h.env = []string{
		"PATH=" + h.bin + ":" + sys,
		"HOME=" + dir,
		"TMPDIR=" + dir,
		"FLOW_BIN=" + filepath.Join(h.bin, "flow"),
		"FLOW_CONNECTOR_TOKEN=tok",
		"FAKE_DIALOG_ARGS=" + h.dialogArgs,
		"FAKE_NOTIFY_ARGS=" + h.notifyArgs,
		"FAKE_FLOW_CALLS=" + h.calls,
		"FAKE_DIALOG_DONE=" + h.done,
		"FAKE_LIST_RESULT=" + tools.listResult,
		"FAKE_ENTRY_RESULT=" + tools.entryResult,
	}
	if tools.listCancel {
		h.env = append(h.env, "FAKE_LIST_CANCEL=1")
	}
	if tools.entryCancel {
		h.env = append(h.env, "FAKE_ENTRY_CANCEL=1")
	}
	if tools.wayland {
		h.env = append(h.env, "WAYLAND_DISPLAY=wayland-0")
	} else if !tools.noDisplay {
		h.env = append(h.env, "DISPLAY=:0")
	}
	return h
}

// run invokes the sample and returns (exit code, stderr). It must return
// fast: anything waiting on the human is forked.
func (h *linuxHarness) run(verb, payload string) (int, string) {
	h.t.Helper()
	cmd := exec.Command(filepath.Join(linuxSample, "run"), verb)
	cmd.Env = h.env
	cmd.Stdin = strings.NewReader(payload)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	err := cmd.Run()
	if d := time.Since(start); d > 1400*time.Millisecond {
		h.t.Errorf("run %s took %v — it must fork the dialog and exit", verb, d)
	}
	rc := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			h.t.Fatalf("run %s: %v", verb, err)
		}
		rc = ee.ExitCode()
	}
	return rc, stderr.String()
}

func (h *linuxHarness) mustRun(verb, payload string) {
	h.t.Helper()
	if rc, se := h.run(verb, payload); rc != 0 {
		h.t.Fatalf("run %s: exit %d\n%s", verb, rc, se)
	}
}

func (h *linuxHarness) flowCall() []string {
	h.t.Helper()
	if !waitFile(h.t, h.calls) {
		h.t.Fatalf("connector never called FLOW_BIN")
	}
	return nulFields(h.t, h.calls)
}

func (h *linuxHarness) noFlowCall() {
	h.t.Helper()
	if !waitFile(h.t, h.done) {
		h.t.Fatalf("no dialog/notification ran")
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(h.calls); err == nil {
		h.t.Errorf("FLOW_BIN called: %q", nulFields(h.t, h.calls))
	}
}

// dialogCalls returns each dialog invocation's argv (split on the "--"
// record separator the fake appends).
func (h *linuxHarness) dialogCalls(n int) [][]string {
	h.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(h.dialogArgs); err == nil && strings.Count(string(b), "--\x00") >= n {
			time.Sleep(50 * time.Millisecond)
			b, _ = os.ReadFile(h.dialogArgs)
			var out [][]string
			for _, rec := range strings.Split(strings.TrimSuffix(string(b), "--\x00"), "--\x00") {
				out = append(out, strings.Split(strings.TrimSuffix(rec, "\x00"), "\x00"))
			}
			return out
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.t.Fatalf("expected %d dialog call(s)", n)
	return nil
}

func (h *linuxHarness) notifyArgv() []string {
	h.t.Helper()
	if !waitFile(h.t, h.notifyArgs) {
		h.t.Fatalf("notify-send never ran")
	}
	return nulFields(h.t, h.notifyArgs)
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestLinuxSampleManifestIsValid(t *testing.T) {
	m, err := connector.Load(linuxSample)
	if err != nil {
		t.Fatalf("sample manifest invalid: %v", err)
	}
	if m.Name != "linux" || !m.Wants("ask") || !m.Wants("urgent") {
		t.Errorf("manifest = %+v", m)
	}
	if !hasArg(availableSamples(), "linux") {
		t.Errorf("linux sample is not embedded: %v", availableSamples())
	}
}

func TestLinuxZenityChoiceAndNote(t *testing.T) {
	h := newLinuxHarness(t, linuxTools{zenity: true, notify: true, listResult: "Yes", entryResult: "after lunch"})
	h.mustRun("send", `{"id":"ab12","from_task":"asker","body":"ship \"v2\"?\nsecond line — ok","options":["Yes","No"],"urgent":false,"kind":"ask","created_at":"2026-11-06T10:00:00Z","reply_hint":"x"}`)
	eqArgs(t, "flow call", h.flowCall(), []string{"human", "reply", "ab12", "Yes — after lunch"})
	calls := h.dialogCalls(2)
	list, note := calls[0], calls[1]
	if !hasArg(list, "--list") || !hasArg(list, "--title=flow - asker") || !hasArg(list, "--text=ship \"v2\"?\nsecond line — ok") {
		t.Errorf("list dialog argv = %q", list)
	}
	if len(list) < 2 || list[len(list)-2] != "Yes" || list[len(list)-1] != "No" {
		t.Errorf("options must be the trailing argv: %q", list)
	}
	if !hasArg(note, "--entry") || !hasArg(note, "--text=You chose: Yes\n\nAdd a note (optional):") {
		t.Errorf("note dialog argv = %q", note)
	}
	if _, err := os.Stat(h.notifyArgs); err == nil {
		t.Errorf("an ask with a dialog must not also toast: %q", nulFields(t, h.notifyArgs))
	}
}

func TestLinuxZenityChoiceWithoutNoteAndUrgent(t *testing.T) {
	h := newLinuxHarness(t, linuxTools{zenity: true, listResult: "No", entryResult: ""})
	h.mustRun("send", `{"id":"ab13","from_task":"asker","body":"q","options":["Yes","No"],"urgent":true,"kind":"ask"}`)
	eqArgs(t, "flow call", h.flowCall(), []string{"human", "reply", "ab13", "No"})
	if list := h.dialogCalls(1)[0]; !hasArg(list, "--text=URGENT: q") {
		t.Errorf("urgent body missing: %q", list)
	}
}

func TestLinuxZenityNoOptionsReplyIsTheText(t *testing.T) {
	h := newLinuxHarness(t, linuxTools{zenity: true, entryResult: "sounds good"})
	h.mustRun("send", `{"id":"ab14","from_task":"","body":"thoughts?","options":[],"urgent":false,"kind":"ask"}`)
	eqArgs(t, "flow call", h.flowCall(), []string{"human", "reply", "ab14", "sounds good"})
	entry := h.dialogCalls(1)[0]
	if !hasArg(entry, "--entry") || !hasArg(entry, "--title=flow") || !hasArg(entry, "--text=thoughts?") || hasArg(entry, "--list") {
		t.Errorf("entry argv = %q", entry)
	}
}

func TestLinuxEmptyReplyMarksSeen(t *testing.T) {
	h := newLinuxHarness(t, linuxTools{zenity: true, entryResult: ""})
	h.mustRun("send", `{"id":"ab15","from_task":"t","body":"fyi-ish","options":[],"kind":"ask"}`)
	eqArgs(t, "flow call", h.flowCall(), []string{"human", "seen", "ab15"})
}

func TestLinuxCancelDoesNothing(t *testing.T) {
	h := newLinuxHarness(t, linuxTools{zenity: true, listCancel: true})
	h.mustRun("send", `{"id":"ab17","from_task":"t","body":"q","options":["Yes","No"],"kind":"ask"}`)
	h.noFlowCall()
}

func TestLinuxCancelOnNoteDoesNothing(t *testing.T) {
	h := newLinuxHarness(t, linuxTools{zenity: true, listResult: "Yes", entryCancel: true})
	h.mustRun("send", `{"id":"ab18","from_task":"t","body":"q","options":["Yes","No"],"kind":"ask"}`)
	h.dialogCalls(2)
	h.noFlowCall()
}

func TestLinuxKdialogWhenNoZenity(t *testing.T) {
	h := newLinuxHarness(t, linuxTools{kdialog: true, listResult: "B", entryResult: "because"})
	h.mustRun("send", `{"id":"ab16","from_task":"t","body":"pick","options":["A","B","C"],"kind":"ask"}`)
	eqArgs(t, "flow call", h.flowCall(), []string{"human", "reply", "ab16", "B — because"})
	calls := h.dialogCalls(2)
	combo, note := calls[0], calls[1]
	if !hasArg(combo, "--combobox") || !hasArg(combo, "--title") || !hasArg(combo, "flow - t") || !hasArg(combo, "pick") {
		t.Errorf("combobox argv = %q", combo)
	}
	if len(combo) < 3 || strings.Join(combo[len(combo)-3:], ",") != "A,B,C" {
		t.Errorf("options must be the trailing argv: %q", combo)
	}
	if !hasArg(note, "--inputbox") {
		t.Errorf("note argv = %q", note)
	}
}

func TestLinuxNotifyOnlyWhenNoDialogTool(t *testing.T) {
	h := newLinuxHarness(t, linuxTools{notify: true})
	h.mustRun("send", `{"id":"ab19","from_task":"deploy","body":"Ship it?","options":["Yes","No"],"kind":"ask"}`)
	h.noFlowCall()
	n := h.notifyArgv()
	joined := strings.Join(n, "\n")
	for _, want := range []string{"flow - deploy", "Ship it?", "Yes | No", "flow inbox"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notification lacks %q: %q", want, n)
		}
	}
}

func TestLinuxNoDisplayFailsClearly(t *testing.T) {
	h := newLinuxHarness(t, linuxTools{zenity: true, notify: true, noDisplay: true})
	rc, se := h.run("send", `{"id":"ab20","from_task":"t","body":"q","options":["Yes"],"kind":"ask"}`)
	if rc != 1 || !strings.Contains(se, "no display") || !strings.Contains(se, "flow inbox") {
		t.Errorf("no-display send: rc=%d stderr=%q", rc, se)
	}
	time.Sleep(300 * time.Millisecond)
	for _, f := range []string{h.dialogArgs, h.notifyArgs, h.calls} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s: nothing may run without a display", filepath.Base(f))
		}
	}
}

// WAYLAND_DISPLAY alone counts as a display.
func TestLinuxWaylandCountsAsDisplay(t *testing.T) {
	h := newLinuxHarness(t, linuxTools{zenity: true, entryResult: "ok", wayland: true})
	h.mustRun("send", `{"id":"ab21","from_task":"t","body":"q","options":[],"kind":"ask"}`)
	eqArgs(t, "flow call", h.flowCall(), []string{"human", "reply", "ab21", "ok"})
}

func TestLinuxNoToolsAtAllFails(t *testing.T) {
	h := newLinuxHarness(t, linuxTools{})
	rc, se := h.run("send", `{"id":"ab22","from_task":"t","body":"q","options":[],"kind":"ask"}`)
	if rc != 1 || !strings.Contains(se, "zenity") || !strings.Contains(se, "kdialog") || !strings.Contains(se, "notify-send") {
		t.Errorf("no tools: rc=%d stderr=%q", rc, se)
	}
}

func TestLinuxTestVerbToastsOnly(t *testing.T) {
	h := newLinuxHarness(t, linuxTools{zenity: true, notify: true})
	h.mustRun("test", `{"id":"test","from_task":"flow","body":"flow connector test","options":[],"kind":"test"}`)
	h.noFlowCall()
	n := h.notifyArgv()
	if !hasArg(n, "flow - test") || !hasArg(n, "flow connector test") {
		t.Errorf("test notification argv = %q", n)
	}
	if _, err := os.Stat(h.dialogArgs); err == nil {
		t.Errorf("test must not pop a dialog when notify-send exists")
	}
}

func TestLinuxTestVerbFallsBackToInfoDialog(t *testing.T) {
	h := newLinuxHarness(t, linuxTools{zenity: true})
	h.mustRun("test", `{"id":"test","from_task":"flow","body":"flow connector test","options":[],"kind":"test"}`)
	h.noFlowCall()
	if info := h.dialogCalls(1)[0]; !hasArg(info, "--info") || !hasArg(info, "--text=flow connector test") {
		t.Errorf("info argv = %q", info)
	}
}

func TestLinuxRejectsBadInput(t *testing.T) {
	h := newLinuxHarness(t, linuxTools{zenity: true, notify: true})
	for verb, payload := range map[string]string{"send": "not json", "bogus": "{}", "test": `{"body":"no id"}`} {
		if rc, _ := h.run(verb, payload); rc == 0 {
			t.Errorf("%s %q: exit 0", verb, payload)
		}
	}
}

// A C/POSIX locale (a bare desktop session, a cron-started environment)
// made zenity reject the title's middle dot and exit without a dialog. The
// titles are ASCII now and the script forces a UTF-8 locale so a non-ASCII
// body (an em dash, an accent) still shows.
func TestLinuxCLocaleStillShowsTheDialog(t *testing.T) {
	h := newLinuxHarness(t, linuxTools{zenity: true, notify: true, listResult: "Yes", entryResult: "d\u00e9j\u00e0 vu"})
	h.env = append(h.env, "LC_ALL=C", "LANG=C")
	h.mustRun("send", `{"id":"ab30","from_task":"asker","body":"ship \u2014 now?","options":["Yes","No"],"kind":"ask"}`)
	eqArgs(t, "flow call", h.flowCall(), []string{"human", "reply", "ab30", "Yes \u2014 d\u00e9j\u00e0 vu"})
	list := h.dialogCalls(2)[0]
	if !hasArg(list, "--title=flow - asker") || !hasArg(list, "--text=ship \u2014 now?") {
		t.Errorf("list dialog argv = %q", list)
	}
	for _, a := range list {
		if strings.HasPrefix(a, "--title=") {
			for _, r := range a {
				if r > 127 {
					t.Errorf("title must be ASCII: %q", a)
				}
			}
		}
	}
}
