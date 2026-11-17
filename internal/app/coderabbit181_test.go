package app

import (
	"sync"
	"testing"
	"time"
)

// CodeRabbit #181 (do.go:176): `flow do <slug>` without --auto from inside
// an auto run (env marker or a running auto_run_status) must not heal.
func TestDoInsideAutoRunDoesNotHeal(t *testing.T) {
	setupFlowRoot(t)
	newDriverWithRewrittenBrief(t, "kid")
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}
	stubClaudeStatOK(t)
	stubITerm(t)
	t.Setenv("FLOW_AUTO_RUN", "kid")
	captureStdout(t, func() { cmdDo([]string{"kid"}) })
	if got := briefOf(t, "kid"); got != rewrittenBrief {
		t.Errorf("flow do inside an auto run must not touch the brief:\n%s", got)
	}

	t.Setenv("FLOW_AUTO_RUN", "")
	newDriverWithRewrittenBrief(t, "run")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET auto_run_status='running' WHERE slug='run'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	captureStdout(t, func() { cmdDo([]string{"run"}) })
	if got := briefOf(t, "run"); got != rewrittenBrief {
		t.Errorf("flow do on a task with a running auto run must not touch the brief:\n%s", got)
	}
}

// CodeRabbit #181 (driver_role.go:176): concurrent healers must append the
// Role section once. The seam widens the read→append window so an
// unserialized implementation reliably double-appends.
func TestEnsureDriverRoleSectionConcurrentAppendsOnce(t *testing.T) {
	setupFlowRoot(t)
	newDriverWithRewrittenBrief(t, "drv")
	old := beforeRoleAppend
	beforeRoleAppend = func() { time.Sleep(50 * time.Millisecond) }
	t.Cleanup(func() { beforeRoleAppend = old })
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ensureDriverRoleSection("drv"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	assertHealedOnce(t, "drv")
}
