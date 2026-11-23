package harness

import (
	"strings"
	"testing"
)

// fakeHarness embeds the interface so it satisfies Harness without
// implementing anything; only Name is called.
type fakeHarness struct{ Harness }

func (fakeHarness) Name() Name { return "fake" }

type fakeSelector struct{ fakeHarness }

func (fakeSelector) SelectsModel() bool { return true }

func TestCheckModelOpts(t *testing.T) {
	cases := []struct {
		name          string
		h             Harness
		model, effort string
		wantErr       string
	}{
		{"empty on unsupported", fakeHarness{}, "", "", ""},
		{"model on unsupported", fakeHarness{}, "opus", "", "does not support --model/--effort"},
		{"effort on unsupported", fakeHarness{}, "", "low", "does not support"},
		{"supported", fakeSelector{}, "claude-opus-5-5[1m]", "high", ""},
		{"quote is fine", fakeSelector{}, "it's", "", ""},
		{"leading dash", fakeSelector{}, "-p", "", "invalid --model"},
		{"whitespace", fakeSelector{}, "", "very high", "invalid --effort"},
		{"double quote", fakeSelector{}, "", `lo"w`, "invalid --effort"},
		{"backslash", fakeSelector{}, `a\b`, "", "invalid --model"},
		{"newline", fakeSelector{}, "a\nb", "", "invalid --model"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckModelOpts(c.h, c.model, c.effort)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err=%v, want containing %q", err, c.wantErr)
			}
		})
	}
}

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
