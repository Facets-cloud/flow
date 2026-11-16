package codex

import (
	"reflect"
	"testing"

	"flow/internal/harness"
)

// quirky needs shell quoting: a quote, a glob and a command substitution.
const quirky = "it's[1m]$(x)"

func TestModelEffortShellCommands(t *testing.T) {
	h := New()
	const pre = "codex -s workspace-write -a on-request resume"
	cases := []struct {
		name   string
		resume bool
		opts   harness.LaunchOpts
		want   string
	}{
		{"launch plain", false, harness.LaunchOpts{}, pre + " " + testThreadID + " 'work'"},
		{"launch model+effort", false, harness.LaunchOpts{Model: "gpt-5.5", Effort: "low"},
			pre + " -m 'gpt-5.5' -c 'model_reasoning_effort=\"low\"' " + testThreadID + " 'work'"},
		{"launch quoted model", false, harness.LaunchOpts{Model: quirky},
			pre + " -m 'it'\\''s[1m]$(x)' " + testThreadID + " 'work'"},
		{"resume plain", true, harness.LaunchOpts{}, pre + " " + testThreadID},
		{"resume effort", true, harness.LaunchOpts{Effort: "high"},
			pre + " -c 'model_reasoning_effort=\"high\"' " + testThreadID},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := h.LaunchCmd(testThreadID, "work", c.opts)
			if c.resume {
				got = h.ResumeCmd(testThreadID, c.opts)
			}
			if got != c.want {
				t.Errorf("\n got=%q\nwant=%q", got, c.want)
			}
		})
	}
}

func TestModelEffortAutoRunArgv(t *testing.T) {
	h := New()
	cases := []struct {
		name string
		opts harness.LaunchOpts
		want []string
	}{
		{"plain", harness.LaunchOpts{SkipPermissions: true},
			[]string{"codex", "exec", "resume", "--dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-hook-trust", testThreadID, "work"}},
		{"model+effort", harness.LaunchOpts{SkipPermissions: true, Model: quirky, Effort: "xhigh"},
			[]string{"codex", "exec", "resume", "-m", quirky, "-c", `model_reasoning_effort="xhigh"`,
				"--dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-hook-trust", testThreadID, "work"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := h.AutoRunArgv(testThreadID, "work", c.opts); !reflect.DeepEqual(got, c.want) {
				t.Errorf("\n got=%q\nwant=%q", got, c.want)
			}
		})
	}
}

// The live-session scan must still find a session whose resume carries
// model flags.
func TestLiveSessionIDsSeesModelFlags(t *testing.T) {
	old := PSRunner
	t.Cleanup(func() { PSRunner = old })
	PSRunner = func() ([]byte, error) {
		return []byte("101 codex exec resume -m gpt-5.5 -c model_reasoning_effort=\"low\" " + testThreadID + " work\n"), nil
	}
	live, err := New().LiveSessionIDs()
	if err != nil || live[testThreadID] != 1 {
		t.Errorf("live=%v err=%v", live, err)
	}
}
