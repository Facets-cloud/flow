package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

// seedDoneTasks inserts n already-done tasks (plus archivedN of them
// archived) so the star-nudge count can be driven to an exact value.
func seedDoneTasks(t *testing.T, n, archivedN int) {
	t.Helper()
	db := openFlowDB(t)
	for i := 0; i < n; i++ {
		slug := fmt.Sprintf("seed-done-%03d", i)
		insertTask(t, db, slug, slug, "done", "medium", "/tmp", nil)
		if i < archivedN {
			if _, err := db.Exec(`UPDATE tasks SET archived_at=? WHERE slug=?`, flowdb.NowISO(), slug); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// closeFreshTask adds a task, gives it a session id and runs `flow done`
// on it, returning the exit code.
func closeFreshTask(t *testing.T, slug string) int {
	t.Helper()
	if rc := cmdAdd([]string{"task", slug, "--slug", slug}); rc != 0 {
		t.Fatalf("add %s rc=%d", slug, rc)
	}
	db := openFlowDB(t)
	if _, err := db.Exec(
		`UPDATE tasks SET session_id=?, session_started=? WHERE slug=?`,
		fakeSessionID(slug), flowdb.NowISO(), slug,
	); err != nil {
		t.Fatal(err)
	}
	return cmdDone([]string{slug})
}

// starNudges returns the human's mail from the star-nudge sender.
func starNudges(t *testing.T) []*flowdb.BusMessage {
	t.Helper()
	db := openFlowDB(t)
	all, err := flowdb.AllForHuman(db, flowdb.BusUser)
	if err != nil {
		t.Fatal(err)
	}
	var out []*flowdb.BusMessage
	for _, m := range all {
		if m.FromAssignee == starNudgeSender && m.FromTaskSlug == "" {
			out = append(out, m)
		}
	}
	return out
}

func starMarkerExists(t *testing.T, root string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, starNudgeMarker))
	return err == nil
}

func TestStarNudgeThreshold(t *testing.T) {
	cases := []struct {
		name      string
		preDone   int // tasks already done before the `flow done` under test
		archived  int // of preDone, how many are archived (do not count)
		quiet     string
		wantNudge bool
	}{
		{name: "24th done: nothing", preDone: 23, wantNudge: false},
		{name: "25th done: one FYI", preDone: 24, wantNudge: true},
		{name: "upgrade case: 40 done, marker absent", preDone: 39, wantNudge: true},
		{name: "archived done tasks do not count", preDone: 24, archived: 1, wantNudge: false},
		{name: "FLOW_QUIET=1 suppresses", preDone: 30, quiet: "1", wantNudge: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := setupFlowRoot(t)
			stubClaudeRunner(t, nil)
			t.Setenv("FLOW_QUIET", tc.quiet)
			seedDoneTasks(t, tc.preDone, tc.archived)

			if rc := closeFreshTask(t, "under-test"); rc != 0 {
				t.Fatalf("done rc=%d", rc)
			}
			got := starNudges(t)
			if tc.wantNudge {
				if len(got) != 1 {
					t.Fatalf("want exactly 1 star nudge, got %d", len(got))
				}
				m := got[0]
				if !strings.Contains(m.Body, "https://github.com/Facets-cloud/flow") {
					t.Errorf("body lacks repo URL: %q", m.Body)
				}
				if !m.FYI {
					t.Errorf("nudge must be FYI (never pinged)")
				}
				if m.Ping {
					t.Errorf("nudge must not ping connectors")
				}
				if m.Kind != "message" || m.ToAssignee != flowdb.BusUser || m.ToTaskSlug != "" {
					t.Errorf("nudge must be a message to the human: kind=%q to=%q/%q", m.Kind, m.ToAssignee, m.ToTaskSlug)
				}
				if !starMarkerExists(t, root) {
					t.Errorf("marker %s not written", starNudgeMarker)
				}
			} else {
				if len(got) != 0 {
					t.Fatalf("want no star nudge, got %d: %q", len(got), got[0].Body)
				}
				if starMarkerExists(t, root) {
					t.Errorf("marker must not exist when no nudge was sent")
				}
			}
		})
	}
}

func TestStarNudgeOnlyOnce(t *testing.T) {
	root := setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	t.Setenv("FLOW_QUIET", "")
	seedDoneTasks(t, 24, 0)

	if rc := closeFreshTask(t, "twenty-fifth"); rc != 0 {
		t.Fatalf("done rc=%d", rc)
	}
	if n := len(starNudges(t)); n != 1 {
		t.Fatalf("after 25th: want 1 nudge, got %d", n)
	}
	if !starMarkerExists(t, root) {
		t.Fatal("marker missing after first nudge")
	}
	// 26th and 27th: marker present, nothing new — even after the human
	// popped the first one.
	if rc := closeFreshTask(t, "twenty-sixth"); rc != 0 {
		t.Fatalf("done rc=%d", rc)
	}
	db := openFlowDB(t)
	for _, m := range starNudges(t) {
		if err := flowdb.DeleteBusMessage(db, m.ID); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	if rc := closeFreshTask(t, "twenty-seventh"); rc != 0 {
		t.Fatalf("done rc=%d", rc)
	}
	if n := len(starNudges(t)); n != 0 {
		t.Fatalf("after pop + 27th: want 0 new nudges, got %d", n)
	}
}

// A nudge that cannot be recorded must never fail `flow done`: the status
// flip is the contract, the nudge is best-effort.
func TestStarNudgeFailureDoesNotFailDone(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("read-only dir not enforceable here")
	}
	root := setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	t.Setenv("FLOW_QUIET", "")
	seedDoneTasks(t, 30, 0)

	stateDir := filepath.Dir(filepath.Join(root, starNudgeMarker))
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o755) })

	if rc := closeFreshTask(t, "under-test"); rc != 0 {
		t.Fatalf("done must exit 0 even when the nudge cannot be written; rc=%d", rc)
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "under-test")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "done" {
		t.Errorf("status: got %q, want done", task.Status)
	}
	if n := len(starNudges(t)); n > 1 {
		t.Errorf("at most one nudge ever, got %d", n)
	}
}
