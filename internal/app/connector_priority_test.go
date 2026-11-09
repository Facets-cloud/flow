package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"flow/internal/connector"
	"flow/internal/flowdb"
)

// marker connector: touches <dir>/<name> on send, then exits rc.
func markerConnector(t *testing.T, dir, name, events string, rc int) {
	t.Helper()
	script := "cat >/dev/null; [ \"$1\" = send ] && echo x >> " + filepath.Join(dir, name) + "; exit " + map[int]string{0: "0", 1: "1"}[rc]
	installFakeConnector(t, name, events, script)
	withSecret(t, name)
}

func fired(dir, name string) int {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "x")
}

// stubDispatchSleep records escalation sleeps instead of sleeping and
// runs hook (if any) in their place.
func stubDispatchSleep(t *testing.T, hook func()) *[]time.Duration {
	t.Helper()
	var got []time.Duration
	orig := dispatchSleep
	dispatchSleep = func(d time.Duration) {
		got = append(got, d)
		if hook != nil {
			hook()
		}
	}
	t.Cleanup(func() { dispatchSleep = orig })
	return &got
}

func setConfig(t *testing.T, cfg connector.Config) {
	t.Helper()
	root, _ := connectorsRoot()
	if err := connector.SaveConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
}

func askRow(t *testing.T, body string, extra ...string) *flowdb.BusMessage {
	t.Helper()
	db := openFlowDB(t)
	if rc := msgRC(t, append([]string{"user", body}, extra...)...); rc != 0 {
		t.Fatalf("send rc=%d", rc)
	}
	return rowByBody(t, db, body)
}

func dispatch(t *testing.T, id string) {
	t.Helper()
	var rc int
	captureStdout(t, func() { rc = cmdConnector([]string{"dispatch", id}) })
	if rc != 0 {
		t.Fatalf("dispatch rc=%d\n%s", rc, readDispatchLog(t))
	}
}

func prioSetup(t *testing.T) string {
	t.Helper()
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "asker", "sid-asker")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sid-asker")
	stubDispatchLauncher(t, nil)
	orig := connectorTimeout
	connectorTimeout = 5 * time.Second
	t.Cleanup(func() { connectorTimeout = orig })
	return t.TempDir()
}

func TestPriorityCLI(t *testing.T) {
	dir := prioSetup(t)
	markerConnector(t, dir, "flowhub", `"ask","urgent"`, 0)
	markerConnector(t, dir, "macos", `"ask","urgent"`, 0)

	for _, c := range []struct {
		args []string
		rc   int
	}{
		{[]string{"priority", "flowhub", "1"}, 0},
		{[]string{"priority", "macos", "2"}, 0},
		{[]string{"priority", "ghost", "1"}, 1}, // not installed
		{[]string{"priority", "macos", "zero"}, 2},
		{[]string{"priority", "macos", "0"}, 2},
		{[]string{"priority", "macos"}, 2},
		{[]string{"escalate", "10m"}, 0},
		{[]string{"escalate", "48h"}, 2},
		{[]string{"escalate", "soon"}, 2},
		{[]string{"escalate"}, 2},
	} {
		if rc, out := connectorRC(t, c.args...); rc != c.rc {
			t.Errorf("connector %v rc=%d want %d (%s)", c.args, rc, c.rc, out)
		}
	}
	root, _ := connectorsRoot()
	cfg, err := connector.LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Priority["flowhub"] != 1 || cfg.Priority["macos"] != 2 || cfg.EscalateAfter != "10m" {
		t.Fatalf("config = %+v", cfg)
	}

	_, out := connectorRC(t, "list")
	for _, want := range []string{"priority=1", "priority=2", "escalate: 10m", "flowhub → macos"} {
		if !strings.Contains(out, want) {
			t.Errorf("list lacks %q:\n%s", want, out)
		}
	}

	if rc, _ := connectorRC(t, "priority", "macos", "--clear"); rc != 0 {
		t.Fatal("clear failed")
	}
	if rc, _ := connectorRC(t, "escalate", "off"); rc != 0 {
		t.Fatal("escalate off failed")
	}
	cfg, _ = connector.LoadConfig(root)
	if _, ok := cfg.Priority["macos"]; ok || cfg.EscalateAfter != "" {
		t.Fatalf("after clear/off config = %+v", cfg)
	}
	_, out = connectorRC(t, "list")
	if !strings.Contains(out, "priority=-") || !strings.Contains(out, "escalate: off") {
		t.Errorf("list after clear:\n%s", out)
	}

	// remove drops the priority entry.
	if rc, _ := connectorRC(t, "remove", "flowhub"); rc != 0 {
		t.Fatal("remove failed")
	}
	cfg, _ = connector.LoadConfig(root)
	if _, ok := cfg.Priority["flowhub"]; ok {
		t.Errorf("remove left priority entry: %+v", cfg)
	}
}

// Tier 1 fails → tier 2 is tried; a successful tier stops the walk.
func TestDispatchFallbackWalk(t *testing.T) {
	dir := prioSetup(t)
	markerConnector(t, dir, "flowhub", `"ask","urgent"`, 1)
	markerConnector(t, dir, "macos", `"ask","urgent"`, 0)
	markerConnector(t, dir, "phone", `"ask","urgent"`, 0)
	setConfig(t, connector.Config{Priority: map[string]int{"flowhub": 1, "macos": 2, "phone": 3}})
	sleeps := stubDispatchSleep(t, nil)

	m := askRow(t, "q1")
	dispatch(t, m.ID)
	if fired(dir, "flowhub") != 1 || fired(dir, "macos") != 1 || fired(dir, "phone") != 0 {
		t.Errorf("fired flowhub=%d macos=%d phone=%d, want 1/1/0", fired(dir, "flowhub"), fired(dir, "macos"), fired(dir, "phone"))
	}
	if len(*sleeps) != 0 {
		t.Errorf("slept with escalation off: %v", *sleeps)
	}
	log := readDispatchLog(t)
	if !strings.Contains(log, m.ID+" flowhub fail → macos ok") {
		t.Errorf("dispatch.log lacks path line:\n%s", log)
	}
	if got, _ := flowdb.GetBusMessageByID(openFlowDB(t), m.ID); got.NotifiedAt == "" {
		t.Errorf("notified_at unset after fallback success")
	}
}

// Same priority = one tier, run in parallel; urgent ignores tiers.
func TestDispatchSameTierAndUrgent(t *testing.T) {
	dir := prioSetup(t)
	markerConnector(t, dir, "a", `"ask","urgent"`, 0)
	markerConnector(t, dir, "b", `"ask","urgent"`, 0)
	markerConnector(t, dir, "c", `"ask","urgent"`, 0)
	setConfig(t, connector.Config{Priority: map[string]int{"a": 1, "b": 1, "c": 2}, EscalateAfter: "10m"})
	m := askRow(t, "q-tier", "--options", "x|y")
	// Mark it seen during the escalation sleep so c stays quiet.
	db := openFlowDB(t)
	stubDispatchSleep(t, func() {
		if _, err := db.Exec(`UPDATE bus_messages SET seen_at=? WHERE id=?`, flowdb.NowISO(), m.ID); err != nil {
			t.Fatal(err)
		}
	})
	dispatch(t, m.ID)
	if fired(dir, "a") != 1 || fired(dir, "b") != 1 || fired(dir, "c") != 0 {
		t.Errorf("tier fired a=%d b=%d c=%d, want 1/1/0", fired(dir, "a"), fired(dir, "b"), fired(dir, "c"))
	}
	if !strings.Contains(readDispatchLog(t), m.ID+" escalate stop: seen") {
		t.Errorf("no escalate-stop line:\n%s", readDispatchLog(t))
	}

	u := askRow(t, "prod down", "--urgent")
	sleeps := stubDispatchSleep(t, nil)
	dispatch(t, u.ID)
	if fired(dir, "a") != 2 || fired(dir, "b") != 2 || fired(dir, "c") != 1 {
		t.Errorf("urgent did not reach every tier: a=%d b=%d c=%d", fired(dir, "a"), fired(dir, "b"), fired(dir, "c"))
	}
	if len(*sleeps) != 0 {
		t.Errorf("urgent escalated: %v", *sleeps)
	}
}

// Escalation: a delivered but unseen ask fires the next tier after the
// delay; a failing escalation tier walks on to the one after.
func TestDispatchEscalation(t *testing.T) {
	dir := prioSetup(t)
	markerConnector(t, dir, "flowhub", `"ask"`, 0)
	markerConnector(t, dir, "slackish", `"ask"`, 1)
	markerConnector(t, dir, "macos", `"ask"`, 0)
	setConfig(t, connector.Config{Priority: map[string]int{"flowhub": 1, "slackish": 2, "macos": 3}, EscalateAfter: "10m"})
	sleeps := stubDispatchSleep(t, nil)

	m := askRow(t, "q-esc")
	dispatch(t, m.ID)
	if fired(dir, "flowhub") != 1 || fired(dir, "slackish") != 1 || fired(dir, "macos") != 1 {
		t.Errorf("fired flowhub=%d slackish=%d macos=%d, want 1/1/1", fired(dir, "flowhub"), fired(dir, "slackish"), fired(dir, "macos"))
	}
	// One sleep after flowhub succeeded; after macos the tiers are exhausted.
	if len(*sleeps) != 1 || (*sleeps)[0] != 10*time.Minute {
		t.Errorf("sleeps = %v, want [10m]", *sleeps)
	}
	log := readDispatchLog(t)
	if !strings.Contains(log, m.ID+" escalate → slackish fail → macos ok") {
		t.Errorf("dispatch.log lacks escalate line:\n%s", log)
	}
}

// No priorities set: one tier, no escalation even if configured — the
// pre-priority behavior.
func TestDispatchNoPrioritiesIsParallel(t *testing.T) {
	dir := prioSetup(t)
	markerConnector(t, dir, "a", `"ask"`, 0)
	markerConnector(t, dir, "b", `"ask"`, 1)
	setConfig(t, connector.Config{EscalateAfter: "10m"})
	sleeps := stubDispatchSleep(t, nil)
	m := askRow(t, "q-flat")
	dispatch(t, m.ID)
	if fired(dir, "a") != 1 || fired(dir, "b") != 1 {
		t.Errorf("flat dispatch fired a=%d b=%d", fired(dir, "a"), fired(dir, "b"))
	}
	if len(*sleeps) != 0 {
		t.Errorf("single tier escalated: %v", *sleeps)
	}
}

// A corrupt config.json must never stop pings: dispatch falls back to
// all-in-parallel and logs the problem.
func TestDispatchCorruptConfigFallsBack(t *testing.T) {
	dir := prioSetup(t)
	markerConnector(t, dir, "a", `"ask"`, 0)
	markerConnector(t, dir, "b", `"ask"`, 0)
	root, _ := connectorsRoot()
	if err := os.WriteFile(filepath.Join(root, connector.ConfigFile), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := askRow(t, "q-corrupt")
	dispatch(t, m.ID)
	if fired(dir, "a") != 1 || fired(dir, "b") != 1 {
		t.Errorf("corrupt config blocked pings a=%d b=%d", fired(dir, "a"), fired(dir, "b"))
	}
	if !strings.Contains(readDispatchLog(t), "config") {
		t.Errorf("corrupt config not logged:\n%s", readDispatchLog(t))
	}
}

// Docs: the contract page, usage, docs/connectors.md and CHANGELOG teach priority,
// fallback and escalation.
func TestPriorityDocs(t *testing.T) {
	ref := strings.Join(strings.Fields(readSkillFile(t, "references/connectors.md")), " ")
	for _, want := range []string{"## Priority and fallback", "flow connector priority <name> <n>",
		"flow connector escalate <dur>|off", "config.json", "a human can actually see it", "24h", "urgent"} {
		if !strings.Contains(ref, want) {
			t.Errorf("references/connectors.md lacks %q", want)
		}
	}
	_, usage := connectorRC(t, "--help")
	for _, want := range []string{"flow connector priority <name> <n>", "flow connector escalate <dur>|off"} {
		if !strings.Contains(usage, want) {
			t.Errorf("connector usage lacks %q", want)
		}
	}
	for file, wants := range map[string][]string{
		"../../docs/connectors.md": {"flow connector priority flowhub 1", "flow connector escalate 10m"},
		"../../CHANGELOG.md":       {"flow connector priority", "flow connector escalate"},
	} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(b), want) {
				t.Errorf("%s lacks %q", file, want)
			}
		}
	}
}
