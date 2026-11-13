package prx

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"flow/internal/harness"
)

// stubBG records each prx invocation and answers with out/err.
func stubBG(t *testing.T, out string, err error) *[][]string {
	t.Helper()
	orig := BGCommandRunner
	t.Cleanup(func() { BGCommandRunner = orig })
	calls := &[][]string{}
	BGCommandRunner = func(workDir string, args []string) ([]byte, error) {
		*calls = append(*calls, append([]string{"dir=" + workDir}, args...))
		return []byte(out), err
	}
	return calls
}

const hostJSON = `{"endpoint":"unix:/x.sock","log":"/x.log","pid":4242,"sessionId":"` + testID + `"}`

func TestSpawnBackground(t *testing.T) {
	calls := stubBG(t, "native: starting\n"+hostJSON+"\n", nil)
	bg := New().(harness.BackgroundLauncher)
	a, err := bg.SpawnBackground("/w", "title", "do it", harness.LaunchOpts{SkipPermissions: true, Inject: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if a.SessionID != testID || a.PID != 4242 || a.ShortID != testID[len(testID)-8:] || a.Cwd != "/w" {
		t.Errorf("agent=%+v", a)
	}
	want := []string{"dir=/w", "run", "-sdk", "-detach", "-cwd", "/w", "-max-turns", "5000",
		"-permission-mode", "yolo", "-prompt", "do it\n\n" + harness.InjectionMarker + "\nx"}
	if !reflect.DeepEqual((*calls)[0], want) {
		t.Errorf("argv=%q\nwant %q", (*calls)[0], want)
	}
}

func TestResumeBackgroundKeepsID(t *testing.T) {
	calls := stubBG(t, hostJSON, nil)
	bg := New().(harness.BackgroundLauncher)
	a, err := bg.ResumeBackground("/w", "title", testID, harness.LaunchOpts{})
	if err != nil || a.SessionID != testID {
		t.Fatalf("agent=%+v err=%v", a, err)
	}
	want := []string{"dir=/w", "run", "-sdk", "-detach", "-cwd", "/w", "-max-turns", "5000", "-session", testID}
	if !reflect.DeepEqual((*calls)[0], want) {
		t.Errorf("argv=%q\nwant %q", (*calls)[0], want)
	}
}

func TestDetachErrors(t *testing.T) {
	bg := New().(harness.BackgroundLauncher)
	stubBG(t, "", errors.New("exit status 1: session is already owned"))
	if _, err := bg.SpawnBackground("/w", "", "p", harness.LaunchOpts{}); err == nil {
		t.Error("spawn swallowed a prx failure")
	}
	stubBG(t, "no json here", nil)
	if _, err := bg.ResumeBackground("/w", "title", testID, harness.LaunchOpts{}); err == nil {
		t.Error("resume accepted output without a session id")
	}
}

// A host started a moment ago owns its session before prx's listing (which
// hides empty sessions) shows it; flow must still see it as running.
func TestBackgroundAgentsIncludesUnlistedOwner(t *testing.T) {
	_, sessions := isolate(t)
	origAlive := PIDAlive
	t.Cleanup(func() { PIDAlive = origAlive })
	PIDAlive = func(pid int) bool { return pid == 99 }
	ownerDir := filepath.Join(sessions, ".owner")
	if err := os.MkdirAll(ownerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(ownerDir, testID+".json"), []byte(`{"sessionId":"`+testID+`","pid":99}`), 0o644)
	os.WriteFile(filepath.Join(ownerDir, "dead.json"), []byte(`{"sessionId":"dead","pid":5}`), 0o644)
	stubBG(t, `[]`, nil)

	agents, err := New().(harness.BackgroundLauncher).BackgroundAgents()
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].SessionID != testID || agents[0].PID != 99 {
		t.Errorf("agents=%+v, want only the live unlisted owner", agents)
	}
}

func TestBackgroundAgents(t *testing.T) {
	isolate(t)
	calls := stubBG(t, `[
	 {"id":"019f0000-0000-7000-8000-0000aaaa1111","name":"n1","status":"working","cwd":"/w","owner":{"pid":77,"endpoint":"unix:/a"}},
	 {"id":"019f0000-0000-7000-8000-0000bbbb2222","name":"n2","status":"completed","cwd":"/v"}
	]`, nil)
	agents, err := New().(harness.BackgroundLauncher).BackgroundAgents()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual((*calls)[0], []string{"dir=", "run", "agents", "-json"}) {
		t.Errorf("argv=%q", (*calls)[0])
	}
	want := []harness.BackgroundAgent{
		{ShortID: "aaaa1111", SessionID: "019f0000-0000-7000-8000-0000aaaa1111", Name: "n1", Cwd: "/w", PID: 77, Status: "working"},
		{ShortID: "bbbb2222", SessionID: "019f0000-0000-7000-8000-0000bbbb2222", Name: "n2", Cwd: "/v", Status: "completed"},
	}
	if !reflect.DeepEqual(agents, want) {
		t.Errorf("agents=%+v\nwant   %+v", agents, want)
	}
}
