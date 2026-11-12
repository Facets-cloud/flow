package app

import (
	"database/sql"
	"testing"

	"flow/internal/harness/codex"
)

// F4 root cause, part 2: `flow do` minted the Codex thread (a full model
// round-trip, often 10-40s) INSIDE its BEGIN IMMEDIATE transaction, so the
// write lock was held for the whole probe and a parallel `flow auto`
// waited past busy_timeout and died with SQLITE_BUSY. The probe must run
// with no write lock held.
func TestCmdDoMintsSessionOutsideWriteTx(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "probe-lock")
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}
	dbPath, err := flowDBPath()
	if err != nil {
		t.Fatal(err)
	}
	const sid = "019ff4b1-6162-7263-974f-f5f1866ad0fe"
	var probeWriteErr error
	probed := false
	old := codex.ProbeRunner
	codex.ProbeRunner = func(string) ([]byte, error) {
		probed = true
		// Another process writes while the probe runs; it must not be
		// blocked by a lock held across the probe.
		other, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(200)")
		if err != nil {
			probeWriteErr = err
		} else {
			_, probeWriteErr = other.Exec(`UPDATE workdirs SET last_used_at='x' WHERE path='/nope'`)
			other.Close()
		}
		return []byte(`{"type":"thread.started","thread_id":"` + sid + `"}`), nil
	}
	t.Cleanup(func() { codex.ProbeRunner = old })
	stubITerm(t)

	if rc := cmdDo([]string{"probe-lock", "--harness", "codex"}); rc != 0 {
		t.Fatalf("cmdDo rc=%d", rc)
	}
	if !probed {
		t.Fatal("probe not called")
	}
	if probeWriteErr != nil {
		t.Errorf("a concurrent write during the Codex probe failed (lock held across probe): %v", probeWriteErr)
	}
	db := openFlowDB(t)
	defer db.Close()
	var got string
	if err := db.QueryRow(`SELECT session_id FROM tasks WHERE slug='probe-lock'`).Scan(&got); err != nil || got != sid {
		t.Errorf("session_id=%q err=%v, want %q", got, err, sid)
	}
}
