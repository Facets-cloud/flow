package desktop

import (
	"net/url"
	"strings"
	"testing"
)

func captureOpen(t *testing.T) *[]string {
	t.Helper()
	orig := OpenURL
	t.Cleanup(func() { OpenURL = orig })
	var got []string
	OpenURL = func(link string) error {
		got = append(got, link)
		return nil
	}
	return &got
}

func TestNewSessionURLRoundTrips(t *testing.T) {
	folder := "/Users/x/My Repo"
	prompt := "Run: flow do --here a&b=c\nthen read the brief"
	u, err := url.Parse(NewSessionURL(folder, prompt))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "claude" || u.Host != "code" || u.Path != "/new" {
		t.Fatalf("link = %s, want claude://code/new", u)
	}
	if got := u.Query().Get("folder"); got != folder {
		t.Errorf("folder = %q, want %q", got, folder)
	}
	if got := u.Query().Get("q"); got != prompt {
		t.Errorf("q = %q, want %q", got, prompt)
	}
}

func TestResumeURL(t *testing.T) {
	id := "9e3ddf03-47bc-4d4c-9e63-6ca56fee072f"
	if got, want := ResumeURL(id), "claude://resume?session="+id; got != want {
		t.Errorf("ResumeURL = %q, want %q", got, want)
	}
}

func TestOpenNewSessionGuards(t *testing.T) {
	got := captureOpen(t)
	cases := []struct {
		name, folder, prompt, wantErr string
	}{
		{"no folder", "", "hi", "needs a folder"},
		{"too long", "/tmp", strings.Repeat("x", MaxPromptLen+1), "over Desktop's"},
		{"slash command", "/tmp", "/flow do x", "cannot start with '/'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := OpenNewSession(c.folder, c.prompt)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, c.wantErr)
			}
		})
	}
	if len(*got) != 0 {
		t.Errorf("guarded calls still opened links: %v", *got)
	}
	if err := OpenNewSession("/tmp", "hello"); err != nil || len(*got) != 1 {
		t.Fatalf("valid open: err=%v links=%v", err, *got)
	}
}

func TestOpenSessionRequiresID(t *testing.T) {
	got := captureOpen(t)
	if err := OpenSession(""); err == nil {
		t.Error("empty session id accepted")
	}
	if err := OpenSession("abc"); err != nil || len(*got) != 1 || (*got)[0] != "claude://resume?session=abc" {
		t.Errorf("OpenSession: err=%v links=%v", err, *got)
	}
}
