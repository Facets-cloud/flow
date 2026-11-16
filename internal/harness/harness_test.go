package harness

import "testing"

func TestWithInjection(t *testing.T) {
	for _, c := range []struct{ prompt, inject, want string }{
		{"p", "", "p"},
		{"", "", ""},
		{"", "x", InjectionMarker + "\nx"},
		{"p", "x", "p\n\n" + InjectionMarker + "\nx"},
	} {
		if got := WithInjection(c.prompt, c.inject); got != c.want {
			t.Errorf("WithInjection(%q,%q)=%q want %q", c.prompt, c.inject, got, c.want)
		}
	}
}
