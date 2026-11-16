package app

import (
	"database/sql"
	"strings"
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

// CodeRabbit #132: two unforced `flow do` calls both read an unbound task
// and probe. If the other one binds (and launches) while this probe runs,
// the in-tx re-read sees ITS session — resuming it blindly skips the
// live-session guard and opens a second writer. Refuse instead (unless
// --force); a re-run then goes through the normal guards.
func TestCmdDoRefusesWhenRacerBindsDuringProbe(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		wantRC bool
	}{
		{"interactive", []string{"race", "--harness", "codex"}, false},
		{"auto", []string{"--auto", "race", "--harness", "codex"}, false},
		{"force resumes", []string{"race", "--harness", "codex", "--force"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupFlowRoot(t)
			seedTask(t, "race")
			for _, h := range allHarnesses() {
				t.Setenv(h.SessionIDEnvVar(), "")
			}
			dbPath, err := flowDBPath()
			if err != nil {
				t.Fatal(err)
			}
			const mine = "019ff4b1-6162-7263-974f-f5f1866ad0fe"
			const racer = "44444444-4444-4444-8444-444444444444"
			old := codex.ProbeRunner
			codex.ProbeRunner = func(string) ([]byte, error) {
				other, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(2000)")
				if err != nil {
					t.Fatal(err)
				}
				defer other.Close()
				if _, err := other.Exec(`UPDATE tasks SET status='in-progress', session_id=?, harness='codex' WHERE slug='race'`, racer); err != nil {
					t.Fatalf("racer bind: %v", err)
				}
				return []byte(`{"type":"thread.started","thread_id":"` + mine + `"}`), nil
			}
			t.Cleanup(func() { codex.ProbeRunner = old })
			spawns, _ := stubITerm(t)
			autoCalls, _, _, _, _ := stubAutoLauncher(t, 99)

			stderr := captureStderr(t)
			rc := cmdDo(tc.args)
			errOut := stderr()
			launched := *spawns + int64(*autoCalls)
			if tc.wantRC {
				if rc != 0 || launched != 1 {
					t.Fatalf("--force: rc=%d launched=%d, want 0/1\n%s", rc, launched, errOut)
				}
			} else {
				if rc == 0 || launched != 0 {
					t.Fatalf("rc=%d launched=%d, want a refusal with no launch\n%s", rc, launched, errOut)
				}
				if !strings.Contains(errOut, racer) {
					t.Errorf("refusal should name the racer's session:\n%s", errOut)
				}
			}
			db := openFlowDB(t)
			defer db.Close()
			var got string
			if err := db.QueryRow(`SELECT session_id FROM tasks WHERE slug='race'`).Scan(&got); err != nil || got != racer {
				t.Errorf("session_id=%q err=%v, want the racer's %q kept", got, err, racer)
			}
		})
	}
}
