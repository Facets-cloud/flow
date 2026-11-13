package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/prx"
)

// prx sessions use flow's foreground-inbox path like Codex: prx's
// `monitor` tool polls instead of streaming and its shell has no
// PRAXIS_SESSION_ID, so it can't run the Monitor loop.

const prxSID = "0b7e3f0a-1c2d-4e5f-8a9b-0c1d2e3f4a5b"

func TestHarnessByNamePrx(t *testing.T) {
	h, err := harnessByName("prx")
	if err != nil || h.Name() != harness.NamePrx {
		t.Fatalf("harnessByName(prx)=%v, %v", h, err)
	}
	if !strings.Contains(registeredHarnessNames(), "prx") {
		t.Errorf("registered names missing prx: %s", registeredHarnessNames())
	}
}

func TestHarnessForSpawnPrefersPrxAmbient(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("PRAXIS_SESSION_ID", prxSID)
	h, err := harnessForSpawn(nil)
	if err != nil || h.Name() != harness.NamePrx {
		t.Fatalf("harnessForSpawn=%v, %v; want prx", h, err)
	}
}

func TestCmdDoHereBindsPrxSession(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "prx-here")
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}
	t.Setenv("PRAXIS_SESSION_ID", prxSID)
	// No transcript yet: binding must refuse.
	if rc := cmdDoHere("prx-here", false); rc == 0 {
		t.Fatal("cmdDoHere bound a prx session with no transcript")
	}
	// prx writes the transcript header when it creates the session.
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".praxis", "agent", "sessions", prxSID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(`{"type":"session","version":3}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rc := cmdDoHere("prx-here", false); rc != 0 {
		t.Fatalf("cmdDoHere rc=%d", rc)
	}
	db := openFlowDB(t)
	defer db.Close()
	task, err := flowdb.GetTask(db, "prx-here")
	if err != nil {
		t.Fatal(err)
	}
	if task.Harness.String != "prx" || task.SessionID.String != prxSID || task.Status != "in-progress" {
		t.Errorf("bound task = %+v, want prx/%s/in-progress", task, prxSID)
	}
}

func TestHookSessionStartPrxArmsNoListener(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "px", "sid-px")
	if _, err := db.Exec(`UPDATE tasks SET harness='prx' WHERE slug='px'`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	ctx := hookRun(t, `{"session_id":"sid-px"}`, cmdHookSessionStart)
	if strings.Contains(ctx, "Monitor(") || strings.Contains(ctx, "arm ONE inbox Monitor") {
		t.Errorf("prx SessionStart carries Monitor wording:\n%s", ctx)
	}
	for _, want := range []string{"prx session", "do NOT arm an inbox listener", "`flow inbox pop` in the foreground"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("prx SessionStart missing %q:\n%s", want, ctx)
		}
	}
}

func TestInboxPopWaitRefusedInPrx(t *testing.T) {
	setupFlowRoot(t)
	db := openFlowDB(t)
	mkBusTask(t, db, "px", "sid-px")
	captureStdout(t, func() { _ = cmdMessage([]string{"user/px", "ping"}) })
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("PRAXIS_SESSION_ID", "sid-px")
	var rc int
	out := captureStdout(t, func() { rc = inboxPop([]string{"--wait", "--timeout", "1", "--json"}) })
	if rc != popExitNoListener || !strings.Contains(out, "prx") {
		t.Fatalf("pop --wait in prx rc=%d out=%s", rc, out)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM bus_messages WHERE body='ping'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("refused pop changed the message status to %q", status)
	}
}

func TestAutoSpawnParentReminderPrxSaysNoMonitor(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("PRAXIS_SESSION_ID", prxSID)
	out := parentListenerReminder()
	if strings.Contains(out, "Monitor") || !strings.Contains(out, "prx parent") {
		t.Errorf("prx parent reminder: %s", out)
	}
}

func TestDonePrxAutoRunOwnTaskDefersSweep(t *testing.T) {
	setupFlowRoot(t)
	seedHarnessTask(t, "px", "prx")
	old := prx.SkipPermissionsRunner
	calls := 0
	prx.SkipPermissionsRunner = func(string) error { calls++; return nil }
	t.Cleanup(func() { prx.SkipPermissionsRunner = old })
	t.Setenv(autoRunEnvVar, "px")

	out := captureStdout(t, func() {
		if rc := cmdDone([]string{"px"}); rc != 0 {
			t.Fatalf("done rc=%d", rc)
		}
	})
	if calls != 0 || !strings.Contains(out, "deferred") {
		t.Errorf("sweep calls=%d (want 0, deferred); out=%q", calls, out)
	}
}

func TestConnectorEnvScrubsPrxSessionID(t *testing.T) {
	t.Setenv("PRAXIS_SESSION_ID", prxSID)
	for _, kv := range connectorEnv() {
		if strings.HasPrefix(kv, "PRAXIS_SESSION_ID=") {
			t.Fatalf("connector env leaks %s", kv)
		}
	}
}
