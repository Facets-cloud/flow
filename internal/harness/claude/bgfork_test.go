package claude

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"flow/internal/harness"
)

// A failed background job's reason lives in ~/.claude/jobs/<short>/state.json
// (`claude agents --json` omits it); BackgroundAgents surfaces it as Detail.
func TestBackgroundAgentsFailedCarriesJobDetail(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "jobs", "5c9292f6")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"),
		[]byte(`{"state":"failed","detail":"source session ec9eb039 not found"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := BGCommandRunner
	BGCommandRunner = func(string, []string) ([]byte, error) {
		return []byte(`[{"id":"5c9292f6","kind":"background","sessionId":"5c9292f6-6246-4005-acfa-655c857e9bb5","state":"failed"},
		  {"id":"aaaaaaaa","kind":"background","sessionId":"aaaaaaaa-0000","state":"done"}]`), nil
	}
	t.Cleanup(func() { BGCommandRunner = old })
	agents, err := New().(harness.BackgroundLauncher).BackgroundAgents()
	if err != nil {
		t.Fatal(err)
	}
	if agents[0].Detail != "source session ec9eb039 not found" {
		t.Errorf("failed agent detail = %q", agents[0].Detail)
	}
	if agents[1].Detail != "" {
		t.Errorf("non-failed agent got a detail: %q", agents[1].Detail)
	}
}

func TestStopBackgroundArgv(t *testing.T) {
	var got []string
	old := BGCommandRunner
	BGCommandRunner = func(_ string, args []string) ([]byte, error) {
		got = args
		return []byte("stopped ec9eb039\n"), nil
	}
	t.Cleanup(func() { BGCommandRunner = old })
	if err := New().(harness.BackgroundStopper).StopBackground("ec9eb039"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"stop", "ec9eb039"}) {
		t.Errorf("argv = %v", got)
	}
}

func TestBackgroundForkSourceFromProcess(t *testing.T) {
	old := PSRunner
	PSRunner = func() ([]byte, error) {
		return []byte("  PID COMMAND\n" +
			"17471 /Users/x/.local/share/claude/versions/2.1.325 --session-id ec9eb039-262e-489a-ba66-739616bb88ff --fork-session --resume 54d7b70f-903b-4af6-95bd-cd87b64ec406 --name saaslabs/x\n" +
			"20080 claude --session-id cb13a0a8 --name y\n"), nil
	}
	t.Cleanup(func() { PSRunner = old })
	fs := New().(harness.BackgroundForkSourcer)
	if src, ok := fs.BackgroundForkSource(harness.BackgroundAgent{PID: 17471}); !ok || src != "54d7b70f-903b-4af6-95bd-cd87b64ec406" {
		t.Errorf("source = %q ok=%v", src, ok)
	}
	if src, ok := fs.BackgroundForkSource(harness.BackgroundAgent{PID: 20080}); ok {
		t.Errorf("non-fork process reported source %q", src)
	}
	if _, ok := fs.BackgroundForkSource(harness.BackgroundAgent{PID: 999}); ok {
		t.Error("absent pid reported a source")
	}
}
