package claude

import (
	"reflect"
	"testing"

	"flow/internal/harness"
)

// quirky needs shell quoting: a quote, a glob and a command substitution.
const quirky = "it's[1m]$(x)"

func TestModelEffortShellCommands(t *testing.T) {
	h := New()
	const sid = "658bf2be-5ae3-4842-a8a4-e0d0b785514d"
	cases := []struct {
		name   string
		resume bool
		opts   harness.LaunchOpts
		want   string
	}{
		{"launch plain", false, harness.LaunchOpts{},
			"claude --session-id " + sid + " 'go'"},
		{"launch model+effort", false, harness.LaunchOpts{Model: "haiku", Effort: "low"},
			"claude --session-id " + sid + " 'go' --model 'haiku' --effort 'low'"},
		{"launch skip+model", false, harness.LaunchOpts{SkipPermissions: true, Model: quirky},
			"claude --session-id " + sid + " 'go' --dangerously-skip-permissions --model 'it'\\''s[1m]$(x)'"},
		{"resume plain", true, harness.LaunchOpts{},
			"claude --resume " + sid},
		{"resume effort only", true, harness.LaunchOpts{Effort: "high"},
			"claude --resume " + sid + " --effort 'high'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := h.LaunchCmd(sid, "go", c.opts)
			if c.resume {
				got = h.ResumeCmd(sid, c.opts)
			}
			if got != c.want {
				t.Errorf("\n got=%q\nwant=%q", got, c.want)
			}
		})
	}
}

func TestModelEffortAutoRunArgv(t *testing.T) {
	h := New()
	const sid = "658bf2be-5ae3-4842-a8a4-e0d0b785514d"
	cases := []struct {
		name string
		opts harness.LaunchOpts
		want []string
	}{
		{"plain", harness.LaunchOpts{SkipPermissions: true},
			[]string{"claude", "--session-id", sid, "-p", "go", "--dangerously-skip-permissions"}},
		{"model+effort", harness.LaunchOpts{SkipPermissions: true, Model: quirky, Effort: "max"},
			[]string{"claude", "--session-id", sid, "-p", "go", "--dangerously-skip-permissions", "--model", quirky, "--effort", "max"}},
		{"resume model", harness.LaunchOpts{Resume: true, Model: "opus"},
			[]string{"claude", "--resume", sid, "-p", "go", "--model", "opus"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := h.AutoRunArgv(sid, "go", c.opts); !reflect.DeepEqual(got, c.want) {
				t.Errorf("\n got=%q\nwant=%q", got, c.want)
			}
		})
	}
}

func TestModelEffortBackgroundArgv(t *testing.T) {
	cases := []struct {
		name   string
		resume bool
		opts   harness.LaunchOpts
		want   []string
	}{
		{"spawn plain", false, harness.LaunchOpts{},
			[]string{"--bg", "--name", "flow/bg-probe", "go"}},
		{"spawn model+effort", false, harness.LaunchOpts{Model: quirky, Effort: "low"},
			[]string{"--bg", "--name", "flow/bg-probe", "go", "--model", quirky, "--effort", "low"}},
		{"resume plain", true, harness.LaunchOpts{},
			[]string{"--bg", "--name", "flow/bg-probe", "--resume", "48d287d9-1ef0-4738-84b9-3110beb988c4"}},
		{"resume model", true, harness.LaunchOpts{SkipPermissions: true, Model: "opus"},
			[]string{"--bg", "--name", "flow/bg-probe", "--resume", "48d287d9-1ef0-4738-84b9-3110beb988c4", "--dangerously-skip-permissions", "--model", "opus"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cap := stubBG(t, realBanner, realAgentsJSON)
			bg := New().(harness.BackgroundLauncher)
			var err error
			if c.resume {
				_, err = bg.ResumeBackground("/w", "flow/bg-probe", "48d287d9-1ef0-4738-84b9-3110beb988c4", c.opts)
			} else {
				_, err = bg.SpawnBackground("/w", "flow/bg-probe", "go", c.opts)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cap.args, c.want) {
				t.Errorf("\n got=%q\nwant=%q", cap.args, c.want)
			}
		})
	}
}
