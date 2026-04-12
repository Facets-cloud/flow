package main

import "testing"

func TestSlugify(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello world", "hello-world"},
		{"Add Auth To Budgeting App", "add-auth-to-budgeting-app"},
		{"Fix bug in login.py!", "fix-bug-in-login-py"},
		{"  too   many   spaces  ", "too-many-spaces"},
		{"--leading and trailing--", "leading-and-trailing"},
	}
	for _, c := range cases {
		got, err := Slugify(c.in)
		if err != nil {
			t.Errorf("Slugify(%q) returned error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSlugifyTruncatesLongNames(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// Exactly 6 words — no truncation.
		{"clarify caas exit plan scope today", "clarify-caas-exit-plan-scope-today"},
		// 7 words — truncated to 6.
		{"clarify caas exit plan scope with jayant", "clarify-caas-exit-plan-scope-with"},
		// 10 words.
		{"one two three four five six seven eight nine ten", "one-two-three-four-five-six"},
	}
	for _, c := range cases {
		got, err := Slugify(c.in)
		if err != nil {
			t.Errorf("Slugify(%q) returned error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Slugify(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSlugifyEmpty(t *testing.T) {
	if _, err := Slugify(""); err == nil {
		t.Error("Slugify(\"\") should return error")
	}
}

func TestSlugifyOnlyPunctuation(t *testing.T) {
	if _, err := Slugify("!!!"); err == nil {
		t.Error("Slugify(\"!!!\") should return error")
	}
}
