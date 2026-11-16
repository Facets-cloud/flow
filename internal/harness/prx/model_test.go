package prx

import (
	"reflect"
	"testing"

	"flow/internal/harness"
)

// quirky needs shell quoting: a quote, a glob and a command substitution.
const quirky = "it's[1m]$(x)"

func TestModelEffortShellCommands(t *testing.T) {
	h := New()
	cases := []struct {
		name   string
		resume bool
		opts   harness.LaunchOpts
		want   string
	}{
		{"launch plain", false, harness.LaunchOpts{}, "prx -session-id " + testID + " -prompt 'work'"},
		{"launch model+effort", false, harness.LaunchOpts{SkipPermissions: true, Model: "opus", Effort: "low"},
			"prx -session-id " + testID + " -permission-mode yolo -model 'opus' -thinking 'low' -prompt 'work'"},
		{"launch quoted model", false, harness.LaunchOpts{Model: quirky},
			"prx -session-id " + testID + " -model 'it'\\''s[1m]$(x)' -prompt 'work'"},
		{"resume plain", true, harness.LaunchOpts{}, "prx -session-id " + testID + " -resume " + testID},
		{"resume effort", true, harness.LaunchOpts{Effort: "high"},
			"prx -session-id " + testID + " -thinking 'high' -resume " + testID},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := h.LaunchCmd(testID, "work", c.opts)
			if c.resume {
				got = h.ResumeCmd(testID, c.opts)
			}
			if got != c.want {
				t.Errorf("\n got=%q\nwant=%q", got, c.want)
			}
		})
	}
}

func TestModelEffortArgv(t *testing.T) {
	h := New()
	if got, want := h.AutoRunArgv(testID, "work", harness.LaunchOpts{Model: quirky, Effort: "max"}),
		[]string{"prx", "run", "-session", testID, "-max-turns", "5000", "-model", quirky, "-thinking", "max", "-prompt", "work"}; !reflect.DeepEqual(got, want) {
		t.Errorf("AutoRunArgv\n got=%q\nwant=%q", got, want)
	}

	calls := stubBG(t, hostJSON+"\n", nil)
	bg := New().(harness.BackgroundLauncher)
	if _, err := bg.SpawnBackground("/w", "t", "go", harness.LaunchOpts{Model: "opus", Effort: "low"}); err != nil {
		t.Fatal(err)
	}
	if _, err := bg.ResumeBackground("/w", "t", testID, harness.LaunchOpts{Model: "opus"}); err != nil {
		t.Fatal(err)
	}
	wantSpawn := []string{"dir=/w", "run", "-sdk", "-detach", "-cwd", "/w", "-max-turns", "5000", "-model", "opus", "-thinking", "low", "-prompt", "go"}
	wantResume := []string{"dir=/w", "run", "-sdk", "-detach", "-cwd", "/w", "-max-turns", "5000", "-model", "opus", "-session", testID}
	if !reflect.DeepEqual((*calls)[0], wantSpawn) {
		t.Errorf("spawn\n got=%q\nwant=%q", (*calls)[0], wantSpawn)
	}
	if !reflect.DeepEqual((*calls)[1], wantResume) {
		t.Errorf("resume\n got=%q\nwant=%q", (*calls)[1], wantResume)
	}
}
