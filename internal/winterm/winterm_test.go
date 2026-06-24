package winterm

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

// decodePowerShellCommand reverses encodePowerShellCommand so tests can
// assert on the script that PowerShell would actually run.
func decodePowerShellCommand(t *testing.T, enc string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("encoded byte length %d is not even (not UTF-16LE)", len(raw))
	}
	u16 := make([]uint16, len(raw)/2)
	for i := range u16 {
		u16[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	return string(utf16.Decode(u16))
}

func TestSpawnTabArgvAndScript(t *testing.T) {
	var got []string
	old := Runner
	Runner = func(args []string) error { got = args; return nil }
	t.Cleanup(func() { Runner = old })

	err := SpawnTab(
		"my-task",
		`C:\Users\alice\code\app`,
		"claude --session-id abc 'do the thing'",
		map[string]string{"FLOW_ROOT": `C:\Users\alice\.flow`},
	)
	if err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}

	joined := strings.Join(got, " ")
	for _, want := range []string{"-w", "0", "new-tab", "--title", "my-task", "-d", "powershell.exe", "-NoExit", "-EncodedCommand"} {
		if !contains(got, want) {
			t.Errorf("argv missing %q; got: %s", want, joined)
		}
	}

	// The encoded command is the last arg.
	enc := got[len(got)-1]
	script := decodePowerShellCommand(t, enc)
	for _, want := range []string{
		`Set-Location -LiteralPath 'C:\Users\alice\code\app'`,
		`$env:FLOW_ROOT = 'C:\Users\alice\.flow'`,
		"claude --session-id abc 'do the thing'",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("decoded script missing %q; got:\n%s", want, script)
		}
	}
}

func TestSpawnTabOmitsEmptyTitleAndCwd(t *testing.T) {
	var got []string
	old := Runner
	Runner = func(args []string) error { got = args; return nil }
	t.Cleanup(func() { Runner = old })

	if err := SpawnTab("", "", "claude --resume xyz", nil); err != nil {
		t.Fatalf("SpawnTab: %v", err)
	}
	if contains(got, "--title") {
		t.Errorf("expected no --title for empty title; got %v", got)
	}
	if contains(got, "-d") {
		t.Errorf("expected no -d for empty cwd; got %v", got)
	}
	script := decodePowerShellCommand(t, got[len(got)-1])
	if strings.Contains(script, "Set-Location") {
		t.Errorf("expected no Set-Location for empty cwd; got:\n%s", script)
	}
	if !strings.Contains(script, "claude --resume xyz") {
		t.Errorf("decoded script missing command; got:\n%s", script)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"plain":           "'plain'",
		"it's":            "'it''s'",
		"a\nb":            "'a\nb'", // single-quoted literals span newlines
		`$x ` + "`" + `y`: "'$x `y'", // no interpolation in single quotes
	}
	for in, want := range cases {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	in := "Set-Location -LiteralPath 'C:\\x'\nclaude --session-id abc 'multi\nline'\n"
	if got := decodePowerShellCommand(t, encodePowerShellCommand(in)); got != in {
		t.Errorf("round-trip mismatch:\n got: %q\nwant: %q", got, in)
	}
}

func TestFocusSessionFallsThrough(t *testing.T) {
	ok, err := FocusSession("abc", "claude")
	if err != nil {
		t.Fatalf("FocusSession err: %v", err)
	}
	if ok {
		t.Error("FocusSession should return false (no wt per-tab query)")
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
