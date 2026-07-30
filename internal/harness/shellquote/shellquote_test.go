package shellquote

import "testing"

func TestQuote_SimpleString(t *testing.T) {
	if got := Quote("hello"); got != "'hello'" {
		t.Errorf("Quote(%q) = %q, want 'hello'", "hello", got)
	}
}

func TestQuote_EmptyString(t *testing.T) {
	if got := Quote(""); got != "''" {
		t.Errorf("Quote(%q) = %q, want ''", "", got)
	}
}

func TestQuote_EmbeddedSingleQuote(t *testing.T) {
	got := Quote("it's")
	want := "'it'\\''s'"
	if got != want {
		t.Errorf("Quote(%q) = %q, want %q", "it's", got, want)
	}
}

func TestQuote_MultipleQuotes(t *testing.T) {
	got := Quote("a'b'c")
	want := "'a'\\''b'\\''c'"
	if got != want {
		t.Errorf("Quote(%q) = %q, want %q", "a'b'c", got, want)
	}
}

func TestQuote_NoSubstitution(t *testing.T) {
	// Characters that don't need escaping pass through unchanged.
	for _, s := range []string{"hello world", "a-b_c.d", "$VAR", "`cmd`"} {
		got := Quote(s)
		want := "'" + s + "'"
		if got != want {
			t.Errorf("Quote(%q) = %q, want %q", s, got, want)
		}
	}
}
