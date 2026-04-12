package main

import (
	"strings"
	"testing"
)

func TestSpawnITermTabBuildsExpectedScript(t *testing.T) {
	var capturedArgs []string
	oldRunner := osascriptRunner
	osascriptRunner = func(args []string) error {
		capturedArgs = args
		return nil
	}
	defer func() { osascriptRunner = oldRunner }()

	err := SpawnITermTab(
		"my title",
		"/tmp/some/dir",
		"claude --resume abc-123",
		map[string]string{"FLOW_TASK": "t", "FLOW_PROJECT": "p"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(capturedArgs) < 2 || capturedArgs[0] != "-e" {
		t.Fatalf("expected -e flag, got %v", capturedArgs)
	}
	script := capturedArgs[1]
	for _, want := range []string{
		"my title",
		"/tmp/some/dir",
		"claude --resume abc-123",
		"FLOW_TASK='t'",
		"FLOW_PROJECT='p'",
		// OSC 0 title escape must be injected into the shell command so the
		// tab title survives the shell's own first-prompt title sequence.
		// Backslashes are doubled inside the AppleScript string literal.
		`printf '\\033]0;%s\\007' 'my title'`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q:\n%s", want, script)
		}
	}
	// OSC printf must run AFTER cd/export so shell prompt state is settled,
	// and BEFORE the main command so it takes effect before claude starts.
	oscIdx := strings.Index(script, `printf '\\033]0;`)
	claudeIdx := strings.Index(script, "claude --resume")
	cdIdx := strings.Index(script, "cd '/tmp/some/dir'")
	if !(cdIdx < oscIdx && oscIdx < claudeIdx) {
		t.Errorf("expected cd < osc-printf < claude in script, got cd=%d osc=%d claude=%d:\n%s",
			cdIdx, oscIdx, claudeIdx, script)
	}
}
