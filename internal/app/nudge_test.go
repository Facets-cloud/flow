package app

import (
	"fmt"
	"io"
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
// on it, returning the exit code and what done printed to STDOUT only
// (the agent reads stdout; warnings go to stderr and must not be there).
func closeFreshTask(t *testing.T, slug string) (int, string) {
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
	var rc int
	out := captureStdoutOnly(t, func() { rc = cmdDone([]string{slug}) })
	return rc, out
}

// captureStdoutOnly redirects os.Stdout (not stderr) during f.
func captureStdoutOnly(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	f()
	w.Close()
	os.Stdout = old
	return <-done
}

// busRowCount is the total number of bus rows: the nudge must write none.
func busRowCount(t *testing.T) int {
	t.Helper()
	db := openFlowDB(t)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM bus_messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func starMarkerExists(t *testing.T, root string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(root, starNudgeMarker))
	return err == nil
}

const (
	starURL      = "https://github.com/Facets-cloud/flow"
	starOnlyTime = "This is the only time flow will say this."
	starHeader   = "note for the user (relay once, in your own words is fine):"
)

func assertStarBlock(t *testing.T, out string) {
	t.Helper()
	for _, want := range []string{starHeader, starURL, starOnlyTime} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	// The block comes last, after all of done's own output, preceded by a
	// blank line.
	if i, j := strings.Index(out, "Marked "), strings.Index(out, "\n\n"+starHeader); j < 0 || j < i {
		t.Errorf("block must follow done's output after one blank line:\n%s", out)
	}
	if !strings.HasSuffix(out, starOnlyTime+"\n") {
		t.Errorf("block must be the last thing on stdout:\n%s", out)
	}
	if strings.Count(out, starHeader) != 1 {
		t.Errorf("block printed %d times, want 1", strings.Count(out, starHeader))
	}
}

func assertNoStarBlock(t *testing.T, out string) {
	t.Helper()
	for _, bad := range []string{starHeader, starURL, "only time"} {
		if strings.Contains(out, bad) {
			t.Errorf("stdout must not contain %q:\n%s", bad, out)
		}
	}
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
		{name: "25th done: prints the block", preDone: 24, wantNudge: true},
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
			busBefore := busRowCount(t)

			rc, out := closeFreshTask(t, "under-test")
			if rc != 0 {
				t.Fatalf("done rc=%d", rc)
			}
			if !strings.Contains(out, "Marked under-test as done") {
				t.Errorf("normal done output missing:\n%s", out)
			}
			if tc.wantNudge {
				assertStarBlock(t, out)
				if !starMarkerExists(t, root) {
					t.Errorf("marker %s not written", starNudgeMarker)
				}
			} else {
				assertNoStarBlock(t, out)
				if starMarkerExists(t, root) {
					t.Errorf("marker must not exist when nothing was printed")
				}
			}
			if n := busRowCount(t); n != busBefore {
				t.Errorf("nudge must write no bus row: bus_messages went %d -> %d", busBefore, n)
			}
		})
	}
}

func TestStarNudgeOnlyOnce(t *testing.T) {
	root := setupFlowRoot(t)
	stubClaudeRunner(t, nil)
	t.Setenv("FLOW_QUIET", "")
	seedDoneTasks(t, 24, 0)

	rc, out := closeFreshTask(t, "twenty-fifth")
	if rc != 0 {
		t.Fatalf("done rc=%d", rc)
	}
	assertStarBlock(t, out)
	if !starMarkerExists(t, root) {
		t.Fatal("marker missing after first nudge")
	}
	for _, slug := range []string{"twenty-sixth", "twenty-seventh"} {
		rc, out := closeFreshTask(t, slug)
		if rc != 0 {
			t.Fatalf("done %s rc=%d", slug, rc)
		}
		assertNoStarBlock(t, out)
	}
	if n := busRowCount(t); n != 0 {
		t.Errorf("nudge must write no bus row, found %d", n)
	}
}

// A nudge whose marker cannot be claimed must never fail `flow done`, and
// must not print the block (the next done would print it again).
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

	rc, out := closeFreshTask(t, "under-test")
	if rc != 0 {
		t.Fatalf("done must exit 0 even when the marker cannot be written; rc=%d", rc)
	}
	assertNoStarBlock(t, out)
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "under-test")
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "done" {
		t.Errorf("status: got %q, want done", task.Status)
	}
	if n := busRowCount(t); n != 0 {
		t.Errorf("nudge must write no bus row, found %d", n)
	}
}
