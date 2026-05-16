package iterm

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestSpawnTabSubmitsCommandWithNewline(t *testing.T) {
	oldRunner := Runner
	var script string
	Runner = func(args []string) error {
		script = strings.Join(args, "\n")
		return nil
	}
	t.Cleanup(func() { Runner = oldRunner })

	if err := SpawnTab("task", "/tmp/work", "flow done fix-sort", map[string]string{"FLOW_ROOT": "/tmp/flow"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, `write text " /bin/sh `) || !strings.Contains(script, `newline yes`) {
		t.Fatalf("iTerm script should submit the command with newline yes:\n%s", script)
	}
	body := readLaunchScriptBody(t, script)
	if !strings.Contains(body, `cd '/tmp/work' || exit`) ||
		!strings.Contains(body, "export FLOW_ROOT='/tmp/flow'\nexec flow done fix-sort") {
		t.Fatalf("launcher script missing command/env:\n%s", body)
	}
	if strings.Contains(body, "exec FLOW_ROOT=") {
		t.Fatalf("launcher script should export env before exec, got:\n%s", body)
	}
}

func readLaunchScriptBody(t *testing.T, script string) string {
	t.Helper()
	match := regexp.MustCompile(`/bin/sh '([^']+)'`).FindStringSubmatch(script)
	if len(match) != 2 {
		t.Fatalf("launcher path not found in script:\n%s", script)
	}
	t.Cleanup(func() { _ = os.Remove(match[1]) })
	data, err := os.ReadFile(match[1])
	if err != nil {
		t.Fatalf("read launcher: %v", err)
	}
	return string(data)
}
