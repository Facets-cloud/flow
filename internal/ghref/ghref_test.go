package ghref

import "testing"

func TestPRTagFromURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "plain PR", url: "https://github.com/acme/app/pull/12", want: "gh-pr:acme/app#12"},
		{name: "review anchor", url: "https://github.com/acme/app/pull/12#pullrequestreview-44", want: "gh-pr:acme/app#12"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := PRTagFromURL(tt.url)
			if !ok {
				t.Fatalf("PRTagFromURL() ok = false")
			}
			if got != tt.want {
				t.Fatalf("tag = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPRTagFromURLRejectsNonPR(t *testing.T) {
	if tag, ok := PRTagFromURL("https://github.com/acme/app/issues/12"); ok {
		t.Fatalf("PRTagFromURL() = %q, true; want false", tag)
	}
}
