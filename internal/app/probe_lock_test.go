package app

import (
	"database/sql"
	"net/url"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/codex"
)

// The Codex probe that mints a session id runs a real `codex exec`, whose
// hooks call flow. If `flow do` holds its flow.db write transaction while
// the probe runs, every hook waits out busy_timeout (minutes per spawn,
// #75). The probe must run with no write lock held: a write from another
// connection during the probe must succeed immediately.
func TestCmdDoMintsCodexSessionOutsideWriteTx(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "probe-lock")
	_, _ = stubITerm(t)
	path, err := flowDBPath()
	if err != nil {
		t.Fatal(err)
	}
	var writeErr error
	probed := false
	old := codex.ProbeRunner
	codex.ProbeRunner = func(string, harness.LaunchOpts) ([]byte, error) {
		probed = true
		q := url.Values{}
		q.Set("_pragma", "busy_timeout(200)")
		other, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
		if err != nil {
			writeErr = err
		} else {
			_, writeErr = other.Exec(`UPDATE tasks SET updated_at=? WHERE slug='probe-lock'`, flowdb.NowISO())
			other.Close()
		}
		return []byte(`{"type":"thread.started","thread_id":"019ff4b1-6162-7263-974f-f5f1866ad0fe"}`), nil
	}
	t.Cleanup(func() { codex.ProbeRunner = old })

	if rc := cmdDo([]string{"probe-lock", "--harness", "codex"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if !probed {
		t.Fatal("probe never ran")
	}
	if writeErr != nil {
		t.Errorf("a flow.db write during the probe failed (flow do held its write lock): %v", writeErr)
	}
	task, _ := flowdb.GetTask(openFlowDB(t), "probe-lock")
	if task.SessionID.String != "019ff4b1-6162-7263-974f-f5f1866ad0fe" || task.Harness.String != "codex" {
		t.Errorf("task = %q/%q", task.SessionID.String, task.Harness.String)
	}
}
