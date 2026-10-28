package rex

import (
	"encoding/base64"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStatusSequence(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	cases := []struct {
		name, state, kind, msg, want string
	}{
		{"working", "working", "", "", "\x1b]7501;state=working:app=claude-code\x1b\\"},
		{"done", "done", "", "", "\x1b]7501;state=done:app=claude-code\x1b\\"},
		{"blocked permission with msg", "blocked", "permission", "Claude needs your permission to use Bash",
			"\x1b]7501;state=blocked:kind=permission:app=claude-code:msg=" + b64("Claude needs your permission to use Bash") + "\x1b\\"},
		{"kind dropped unless blocked", "working", "permission", "", "\x1b]7501;state=working:app=claude-code\x1b\\"},
		{"clear carries no app", "clear", "", "", "\x1b]7501;state=clear\x1b\\"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := StatusSequence(tc.state, tc.kind, tc.msg)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestStatusSequenceRejectsInvalid(t *testing.T) {
	if _, err := StatusSequence("busy", "", ""); err == nil {
		t.Error("unknown state must be rejected")
	}
	if _, err := StatusSequence("blocked", "nope", ""); err == nil {
		t.Error("unknown kind must be rejected")
	}
}

// TestStatusSequenceWithinProtocolLimits — the whole report must stay
// under the protocol's 4096-byte sequence cap even with a huge message,
// and the message must decode to valid UTF-8 with no control chars.
func TestStatusSequenceWithinProtocolLimits(t *testing.T) {
	huge := strings.Repeat("é", 3000) + "\x07\x1b[31m\nnext"
	seq, err := StatusSequence("blocked", "question", huge)
	if err != nil {
		t.Fatal(err)
	}
	if len(seq) > 4096 {
		t.Errorf("sequence is %d bytes; protocol cap is 4096", len(seq))
	}
	i := strings.Index(seq, "msg=")
	enc := strings.TrimSuffix(seq[i+4:], "\x1b\\")
	dec, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("msg not base64: %v", err)
	}
	if len(dec) > maxStatusMsg || !utf8.Valid(dec) {
		t.Errorf("decoded msg %d bytes, valid utf8=%v", len(dec), utf8.Valid(dec))
	}
	for _, r := range string(dec) {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("decoded msg contains control char %U", r)
		}
	}
}

func TestSanitizeStatusMsg(t *testing.T) {
	cases := map[string]string{
		"  Needs\n\tyour   approval  ": "Needs your approval",
		"bell\x07 esc\x1b[0m":          "bell esc[0m",
		"":                             "",
		"\n\n":                         "",
	}
	for in, want := range cases {
		if got := sanitizeStatusMsg(in); got != want {
			t.Errorf("sanitize(%q) = %q; want %q", in, got, want)
		}
	}
}

// stubProcTree serves ProcTTY from a pid → (ppid, tty) table.
func stubProcTree(t *testing.T, tree map[int][2]any) {
	t.Helper()
	old := ProcTTY
	ProcTTY = func(pid int) (int, string, error) {
		e, ok := tree[pid]
		if !ok {
			return 0, "", errors.New("no such process")
		}
		return e[0].(int), e[1].(string), nil
	}
	t.Cleanup(func() { ProcTTY = old })
}

func TestFindAncestorTTY(t *testing.T) {
	// hook(100, no tty) → sh(90, no tty) → claude(80, ttys036)
	stubProcTree(t, map[int][2]any{
		100: {90, "??"}, 90: {80, "??"}, 80: {1, "ttys036"},
	})
	if got := findAncestorTTY(100); got != "/dev/ttys036" {
		t.Errorf("got %q; want /dev/ttys036", got)
	}
}

func TestFindAncestorTTYNone(t *testing.T) {
	stubProcTree(t, map[int][2]any{100: {90, "??"}, 90: {1, ""}})
	if got := findAncestorTTY(100); got != "" {
		t.Errorf("got %q; want none", got)
	}
}

// TestFindAncestorTTYBounded — a pathological ppid cycle can't spin.
func TestFindAncestorTTYBounded(t *testing.T) {
	calls := 0
	old := ProcTTY
	ProcTTY = func(pid int) (int, string, error) { calls++; return pid + 1, "??", nil }
	t.Cleanup(func() { ProcTTY = old })
	if got := findAncestorTTY(100); got != "" || calls > maxAncestors {
		t.Errorf("got %q after %d calls; want none within %d", got, calls, maxAncestors)
	}
}

type captureWriter struct{ strings.Builder }

func (c *captureWriter) Close() error { return nil }

func TestReportStatusWritesToClaudeTTY(t *testing.T) {
	t.Setenv("REX_SESSION", "session:s1")
	ppid := os.Getppid()
	stubProcTree(t, map[int][2]any{ppid: {1, "ttys042"}})
	var opened string
	w := &captureWriter{}
	old := OpenTTY
	OpenTTY = func(path string) (io.WriteCloser, error) { opened = path; return w, nil }
	t.Cleanup(func() { OpenTTY = old })

	if err := ReportStatus("done", "", ""); err != nil {
		t.Fatal(err)
	}
	if opened != "/dev/ttys042" {
		t.Errorf("opened %q; want /dev/ttys042", opened)
	}
	if w.String() != "\x1b]7501;state=done:app=claude-code\x1b\\" {
		t.Errorf("wrote %q", w.String())
	}
}

func TestReportStatusOutsideRexIsNoop(t *testing.T) {
	t.Setenv("REX_SESSION", "")
	old, oldP := OpenTTY, ProcTTY
	OpenTTY = func(string) (io.WriteCloser, error) { t.Error("must not open a tty outside Rex"); return nil, nil }
	ProcTTY = func(int) (int, string, error) { t.Error("must not inspect processes outside Rex"); return 0, "", nil }
	t.Cleanup(func() { OpenTTY, ProcTTY = old, oldP })
	if err := ReportStatus("working", "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestReportStatusNoTTYIsNoop(t *testing.T) {
	t.Setenv("REX_SESSION", "session:s1")
	stubProcTree(t, map[int][2]any{os.Getppid(): {1, "??"}})
	old := OpenTTY
	OpenTTY = func(string) (io.WriteCloser, error) { t.Error("must not open without a tty"); return nil, nil }
	t.Cleanup(func() { OpenTTY = old })
	if err := ReportStatus("working", "", ""); err != nil {
		t.Fatal(err)
	}
}

// TestOpenTTYRealDevice — the real opener (with O_NOCTTY) works on a
// character device.
func TestOpenTTYRealDevice(t *testing.T) {
	w, err := OpenTTY("/dev/null")
	if err != nil {
		t.Fatalf("OpenTTY(/dev/null): %v", err)
	}
	if _, err := io.WriteString(w, "x"); err != nil {
		t.Error(err)
	}
	w.Close()
}

// TestProcTTYRealProcess — the real ps-based lookup parses this test
// process (no tty under `go test` in CI, a tty when run from a terminal).
func TestProcTTYRealProcess(t *testing.T) {
	ppid, _, err := ProcTTY(os.Getpid())
	if err != nil {
		t.Fatalf("ProcTTY(self): %v", err)
	}
	if ppid != os.Getppid() {
		t.Errorf("ppid = %d; want %d", ppid, os.Getppid())
	}
}
