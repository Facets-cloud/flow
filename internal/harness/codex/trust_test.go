package codex

import (
	"strings"
	"testing"

	"flow/internal/harness"
)

const trustArg = `projects."/f/.flow/tasks/t/workspace".trust_level="trusted"`

func TestTrustOverrideOnlyWhenTrustDirSet(t *testing.T) {
	c := New()
	with := harness.LaunchOpts{TrustDir: "/f/.flow/tasks/t/workspace"}
	without := harness.LaunchOpts{}

	argv := c.AutoRunArgv(testThreadID, "p", harness.LaunchOpts{SkipPermissions: true, TrustDir: with.TrustDir})
	if !pairArg(argv, "-c", trustArg) {
		t.Errorf("AutoRunArgv missing trust override: %v", argv)
	}
	// -c rides on `exec`, before `resume`, so the ps-matched
	// `resume … <id>` tail never contains a (possibly spaced) path.
	if idx(argv, "-c") > idx(argv, "resume") {
		t.Errorf("-c must precede resume: %v", argv)
	}
	if strings.Contains(strings.Join(c.AutoRunArgv(testThreadID, "p", without), " "), "trust_level") {
		t.Error("AutoRunArgv carries trust without TrustDir")
	}

	for name, cmd := range map[string]string{
		"LaunchCmd": c.LaunchCmd(testThreadID, "p", with),
		"ResumeCmd": c.ResumeCmd(testThreadID, with),
	} {
		if !strings.Contains(cmd, "-c '"+trustArg+"'") {
			t.Errorf("%s missing quoted trust override: %s", name, cmd)
		}
		if strings.Index(cmd, "-c ") > strings.Index(cmd, " resume ") {
			t.Errorf("%s: -c must come before resume: %s", name, cmd)
		}
	}
	for name, cmd := range map[string]string{
		"LaunchCmd": c.LaunchCmd(testThreadID, "p", without),
		"ResumeCmd": c.ResumeCmd(testThreadID, without),
	} {
		if strings.Contains(cmd, "trust_level") {
			t.Errorf("%s carries trust without TrustDir: %s", name, cmd)
		}
	}
}

func TestLiveSessionIDsWithTrustOverride(t *testing.T) {
	orig := PSRunner
	t.Cleanup(func() { PSRunner = orig })
	argv := New().AutoRunArgv(testThreadID, "p", harness.LaunchOpts{SkipPermissions: true, TrustDir: "/a b/.flow/tasks/x/workspace"})
	PSRunner = func() ([]byte, error) { return []byte("123 " + strings.Join(argv, " ") + "\n"), nil }
	live, err := New().LiveSessionIDs()
	if err != nil || live[testThreadID] != 1 {
		t.Fatalf("live=%v err=%v", live, err)
	}
}

func TestTrustOverrideRefusesUnquotablePath(t *testing.T) {
	cmd := New().LaunchCmd(testThreadID, "p", harness.LaunchOpts{TrustDir: `/a"b`})
	if strings.Contains(cmd, "trust_level") {
		t.Errorf("unquotable path must be skipped: %s", cmd)
	}
}

func pairArg(argv []string, k, v string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == k && argv[i+1] == v {
			return true
		}
	}
	return false
}

func idx(argv []string, s string) int {
	for i, a := range argv {
		if a == s {
			return i
		}
	}
	return -1
}
