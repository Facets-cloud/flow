package codex

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/harness"
)

func TestParseSessionID(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{
			name: "json session id",
			out:  `{"type":"session.started","session_id":"codex-sid-123"}`,
			want: "codex-sid-123",
		},
		{
			name: "nested thread id",
			out:  `{"payload":{"thread_id":"abc:def.123"}}`,
			want: "abc:def.123",
		},
		{
			name: "codex session meta",
			out:  `{"type":"session_meta","payload":{"id":"019cf7d0-edec-7b00-9268-ffb5d19c921b","cwd":"/tmp/repo"}}`,
			want: "019cf7d0-edec-7b00-9268-ffb5d19c921b",
		},
		{
			name: "labeled text",
			out:  `created session id: 658bf2be-5ae3-4842-a8a4-e0d0b785514d`,
			want: "658bf2be-5ae3-4842-a8a4-e0d0b785514d",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSessionID([]byte(tc.out))
			if err != nil {
				t.Fatalf("ParseSessionID: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewSessionIDRunsCodexExecProbe(t *testing.T) {
	old := ExecRunner
	t.Cleanup(func() { ExecRunner = old })
	var gotArgs []string
	ExecRunner = func(args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte(`{"session_id":"codex-probe-sid"}`), nil
	}

	id, err := New().NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	if id != "codex-probe-sid" {
		t.Fatalf("id = %q", id)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"exec", "--json", "--dangerously-bypass-approvals-and-sandbox"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("probe args %q missing %q", joined, want)
		}
	}
}

func TestLaunchAndResumeCmdUseCodexResume(t *testing.T) {
	h := New()
	got := h.LaunchCmd("sid-123", "do work", harness.LaunchOpts{})
	if got != "codex resume sid-123 'do work'" {
		t.Fatalf("LaunchCmd = %q", got)
	}

	got = h.LaunchCmd("sid-123", "do work", harness.LaunchOpts{SkipPermissions: true, Inject: "extra"})
	if !strings.HasPrefix(got, "codex resume --dangerously-bypass-approvals-and-sandbox sid-123 ") {
		t.Fatalf("LaunchCmd skip prefix = %q", got)
	}
	if !strings.Contains(got, harness.InjectionMarker+"\nextra") {
		t.Fatalf("LaunchCmd missing injection marker: %q", got)
	}

	got = h.ResumeCmd("sid-123", harness.LaunchOpts{Inject: "follow up"})
	want := "codex resume sid-123 '" + harness.InjectionMarker + "\nfollow up'"
	if got != want {
		t.Fatalf("ResumeCmd = %q, want %q", got, want)
	}
}

func TestLiveSessionIDs(t *testing.T) {
	old := PSRunner
	t.Cleanup(func() { PSRunner = old })
	PSRunner = func() ([]byte, error) {
		return []byte(`  PID COMMAND
1 codex resume codex-sid-1
2 /usr/bin/codex resume --dangerously-bypass-approvals-and-sandbox codex-sid-1
3 codex exec whatever
4 other resume codex-sid-2
`), nil
	}
	live, err := New().LiveSessionIDs()
	if err != nil {
		t.Fatal(err)
	}
	if live["codex-sid-1"] != 2 || len(live) != 1 {
		t.Fatalf("live = %#v", live)
	}
}

func TestLiveSessionIDsPSError(t *testing.T) {
	old := PSRunner
	t.Cleanup(func() { PSRunner = old })
	PSRunner = func() ([]byte, error) { return nil, errors.New("ps failed") }
	if _, err := New().LiveSessionIDs(); err == nil {
		t.Fatal("expected error")
	}
}

func TestSkillAndHookInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	h := New()

	if err := h.InstallSkill([]byte("flow skill")); err != nil {
		t.Fatalf("InstallSkill: %v", err)
	}
	skillPath := filepath.Join(home, ".codex", "skills", "flow", "SKILL.md")
	if got, err := os.ReadFile(skillPath); err != nil || string(got) != "flow skill" {
		t.Fatalf("skill = %q, err=%v", got, err)
	}

	added, err := h.InstallSessionStartHook("flow hook session-start")
	if err != nil {
		t.Fatalf("InstallSessionStartHook: %v", err)
	}
	if !added {
		t.Fatal("first hook install should report added")
	}
	config, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "codex_hooks = true") {
		t.Fatalf("config missing codex_hooks=true:\n%s", config)
	}
	hooks, err := os.ReadFile(filepath.Join(home, ".codex", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hooks), "flow hook session-start") {
		t.Fatalf("hooks missing command:\n%s", hooks)
	}

	added, err = h.InstallSessionStartHook("flow hook session-start")
	if err != nil {
		t.Fatalf("second InstallSessionStartHook: %v", err)
	}
	if added {
		t.Fatal("second hook install should be idempotent")
	}

	removed, err := h.UninstallSessionStartHook("flow hook session-start")
	if err != nil {
		t.Fatalf("UninstallSessionStartHook: %v", err)
	}
	if !removed {
		t.Fatal("uninstall should report removed")
	}
}
