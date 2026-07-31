package shellquote

import (
	"os/exec"
	"testing"
)

func TestQuote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple", "hello", "'hello'"},
		{"empty", "", "''"},
		{"space", "with space", "'with space'"},
		{"embedded quote", "it's", `'it'\''s'`},
		{"multiple quotes", "a'b'c", `'a'\''b'\''c'`},
		{"only a quote", "'", `''\'''`},
		{"backslash", `back\slash`, `'back\slash'`},
		{"dollar var", "$VAR", "'$VAR'"},
		{"backticks", "`cmd`", "'`cmd`'"},
		{"double quote", `say "hi"`, `'say "hi"'`},
		{"newline", "a\nb", "'a\nb'"},
		{"semicolon and pipe", "a; rm -rf /|b", "'a; rm -rf /|b'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Quote(tt.in); got != tt.want {
				t.Errorf("Quote(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The contract that matters is not the byte shape but that a POSIX shell
// reads the quoted form back as the original string, whatever it holds.
// Every terminal backend interpolates Quote's output into a command line
// it hands to a real shell, so verify against one.
func TestQuoteRoundTripsThroughShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	inputs := []string{
		"plain",
		"with space",
		"it's",
		`'`,
		`back\slash`,
		"$HOME",
		"`whoami`",
		"$(whoami)",
		`say "hi"`,
		"a; echo INJECTED",
		"trailing ",
		"emoji 🚀 and ünïcode",
	}
	for _, in := range inputs {
		out, err := exec.Command(sh, "-c", "printf %s "+Quote(in)).Output()
		if err != nil {
			t.Errorf("sh rejected Quote(%q) = %s: %v", in, Quote(in), err)
			continue
		}
		if string(out) != in {
			t.Errorf("round trip of %q through the shell gave %q (quoted: %s)", in, out, Quote(in))
		}
	}
}
