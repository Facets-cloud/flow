package claude

import (
	"reflect"
	"strings"
	"testing"

	"flow/internal/harness"
)

// realBanner mirrors the exact stdout `claude --bg --name <n> <prompt>`
// prints (captured from the live CLI): the short id is wrapped in ANSI
// color codes, the separator is U+00B7, and help lines follow that also
// mention the short id (so the parser must read only the first line).
const realBanner = "backgrounded · \x1b[36m48d287d9\x1b[39m · flow/bg-probe\n" +
	"\x1b[2m  claude agents             list sessions\x1b[22m\n" +
	"\x1b[2m  claude attach 48d287d9    open in this terminal\x1b[22m\n"

// realAgentsJSON mirrors `claude agents --json` (captured live).
const realAgentsJSON = `[
  {"pid":7184,"id":"48d287d9","cwd":"/work/dir","kind":"background",
   "startedAt":1781025646235,"sessionId":"48d287d9-1ef0-4738-84b9-3110beb988c4",
   "name":"flow/bg-probe","status":"idle","state":"done"},
  {"pid":85723,"id":"5cb25e62","cwd":"/other","kind":"background",
   "startedAt":1780564604460,"sessionId":"5cb25e62-0f1f-40d9-9da6-72b2d36c990f",
   "name":"Monitoring","status":"busy","state":"working"}
]`

func TestParseBackgroundBanner(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		err  bool
	}{
		{"ansi+name", realBanner, "48d287d9", false},
		{"no-ansi", "backgrounded · c10d7528 · some name\n", "c10d7528", false},
		{"no-name", "backgrounded · \x1b[36m1923df79\x1b[39m\n", "1923df79", false},
		{"no-banner", "some unrelated output\n", "", true},
		{"empty", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseBackgroundBanner(tc.in)
			if tc.err {
				if err == nil {
					t.Fatalf("parseBackgroundBanner(%q) = %q, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBackgroundBanner(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("parseBackgroundBanner: got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseBackgroundAgents(t *testing.T) {
	agents, err := parseBackgroundAgents([]byte(realAgentsJSON))
	if err != nil {
		t.Fatalf("parseBackgroundAgents: %v", err)
	}
	want := []harness.BackgroundAgent{
		{
			ShortID:   "48d287d9",
			SessionID: "48d287d9-1ef0-4738-84b9-3110beb988c4",
			Name:      "flow/bg-probe",
			Cwd:       "/work/dir",
			PID:       7184,
			Status:    "idle",
			State:     "done",
		},
		{
			ShortID:   "5cb25e62",
			SessionID: "5cb25e62-0f1f-40d9-9da6-72b2d36c990f",
			Name:      "Monitoring",
			Cwd:       "/other",
			PID:       85723,
			Status:    "busy",
			State:     "working",
		},
	}
	if !reflect.DeepEqual(agents, want) {
		t.Errorf("parseBackgroundAgents mismatch:\n got %+v\nwant %+v", agents, want)
	}
}

// stubBG installs a BGCommandRunner that dispatches on args[0]/presence
// of --bg: spawn/resume calls get the banner, `agents --json` gets the
// JSON. Records the last spawn/resume argv for assertions.
func stubBG(t *testing.T, banner, agentsJSON string) *[]string {
	t.Helper()
	var lastNonAgents []string
	old := BGCommandRunner
	BGCommandRunner = func(args []string) ([]byte, error) {
		if len(args) >= 1 && args[0] == "agents" {
			return []byte(agentsJSON), nil
		}
		lastNonAgents = args
		return []byte(banner), nil
	}
	t.Cleanup(func() { BGCommandRunner = old })
	return &lastNonAgents
}

func TestSpawnBackgroundCapturesSessionID(t *testing.T) {
	argv := stubBG(t, realBanner, realAgentsJSON)
	h := New().(harness.BackgroundLauncher)

	agent, err := h.SpawnBackground("flow/bg-probe", "do the work", harness.LaunchOpts{})
	if err != nil {
		t.Fatalf("SpawnBackground: %v", err)
	}
	if agent.SessionID != "48d287d9-1ef0-4738-84b9-3110beb988c4" {
		t.Errorf("SessionID: got %q, want full UUID", agent.SessionID)
	}
	if agent.Name != "flow/bg-probe" {
		t.Errorf("Name: got %q", agent.Name)
	}
	if agent.Status != "idle" {
		t.Errorf("Status: got %q, want idle", agent.Status)
	}

	// argv must carry --bg, --name <name>, and the prompt; no skip flag.
	joined := strings.Join(*argv, "\x00")
	if !strings.Contains(joined, "--bg") {
		t.Errorf("spawn argv missing --bg: %v", *argv)
	}
	if !containsPair(*argv, "--name", "flow/bg-probe") {
		t.Errorf("spawn argv missing --name flow/bg-probe: %v", *argv)
	}
	if !contains(*argv, "do the work") {
		t.Errorf("spawn argv missing prompt: %v", *argv)
	}
	if contains(*argv, "--dangerously-skip-permissions") {
		t.Errorf("spawn argv should NOT skip permissions when opts unset: %v", *argv)
	}
}

func TestSpawnBackgroundSkipPermissions(t *testing.T) {
	argv := stubBG(t, realBanner, realAgentsJSON)
	h := New().(harness.BackgroundLauncher)
	if _, err := h.SpawnBackground("n", "p", harness.LaunchOpts{SkipPermissions: true}); err != nil {
		t.Fatalf("SpawnBackground: %v", err)
	}
	if !contains(*argv, "--dangerously-skip-permissions") {
		t.Errorf("spawn argv missing skip flag: %v", *argv)
	}
}

func TestSpawnBackgroundInjectAppended(t *testing.T) {
	argv := stubBG(t, realBanner, realAgentsJSON)
	h := New().(harness.BackgroundLauncher)
	if _, err := h.SpawnBackground("n", "base prompt", harness.LaunchOpts{Inject: "also check X"}); err != nil {
		t.Fatalf("SpawnBackground: %v", err)
	}
	joined := strings.Join(*argv, "\n")
	if !strings.Contains(joined, harness.InjectionMarker) || !strings.Contains(joined, "also check X") {
		t.Errorf("spawn argv missing injection marker/text: %v", *argv)
	}
}

// If the captured short id isn't in the agents registry, capture failed
// — surface an error rather than recording a phantom id.
func TestSpawnBackgroundShortIDNotInRegistry(t *testing.T) {
	stubBG(t, "backgrounded · deadbeef · n\n", realAgentsJSON)
	h := New().(harness.BackgroundLauncher)
	if _, err := h.SpawnBackground("n", "p", harness.LaunchOpts{}); err == nil {
		t.Fatalf("SpawnBackground: want error when short id absent from registry")
	}
}

// ResumeBackground resumes the OLD id under --bg but, because --bg mints
// a fresh id, must return the NEW captured agent (history inherited).
func TestResumeBackgroundCapturesNewID(t *testing.T) {
	argv := stubBG(t, realBanner, realAgentsJSON)
	h := New().(harness.BackgroundLauncher)
	oldSID := "00000000-1111-4222-8333-444444444444"
	agent, err := h.ResumeBackground(oldSID, harness.LaunchOpts{SkipPermissions: true})
	if err != nil {
		t.Fatalf("ResumeBackground: %v", err)
	}
	// argv resumes the OLD id under --bg ...
	if !contains(*argv, "--bg") || !containsPair(*argv, "--resume", oldSID) {
		t.Errorf("resume argv wrong: %v", *argv)
	}
	if !contains(*argv, "--dangerously-skip-permissions") {
		t.Errorf("resume argv missing skip flag: %v", *argv)
	}
	// ... but returns the NEW id minted by --bg (captured via banner + registry).
	if agent.SessionID != "48d287d9-1ef0-4738-84b9-3110beb988c4" {
		t.Errorf("ResumeBackground returned %q, want the newly-minted id", agent.SessionID)
	}
}

func TestBackgroundAgentsList(t *testing.T) {
	stubBG(t, realBanner, realAgentsJSON)
	h := New().(harness.BackgroundLauncher)
	agents, err := h.BackgroundAgents()
	if err != nil {
		t.Fatalf("BackgroundAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("BackgroundAgents: got %d, want 2", len(agents))
	}
}

// BackgroundAgents must query with --all so exited/failed/completed
// sessions are visible (needed to tell "removed" from "present, idle").
func TestBackgroundAgentsUsesAll(t *testing.T) {
	var gotArgs []string
	old := BGCommandRunner
	BGCommandRunner = func(args []string) ([]byte, error) { gotArgs = args; return []byte("[]"), nil }
	t.Cleanup(func() { BGCommandRunner = old })
	if _, err := New().(harness.BackgroundLauncher).BackgroundAgents(); err != nil {
		t.Fatalf("BackgroundAgents: %v", err)
	}
	if !contains(gotArgs, "agents") || !contains(gotArgs, "--json") || !contains(gotArgs, "--all") {
		t.Errorf("BackgroundAgents args = %v, want agents --json --all", gotArgs)
	}
}

// ---- tiny helpers ----

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func containsPair(ss []string, flag, val string) bool {
	for i := 0; i+1 < len(ss); i++ {
		if ss[i] == flag && ss[i+1] == val {
			return true
		}
	}
	return false
}
