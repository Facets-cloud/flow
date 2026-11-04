package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flow/internal/flowdb"
)

// delegationMarker is the heading the SessionStart hook prints in a bound
// driver session. Tests key on it so wording around it can evolve.
const delegationMarker = "flow driver delegation"

func sessionStartOutput(t *testing.T) string {
	t.Helper()
	var rc int
	out := captureStdout(t, func() { rc = cmdHookSessionStart(nil) })
	if rc != 0 {
		t.Fatalf("hook session-start rc=%d", rc)
	}
	return out
}

func TestSessionStartBoundDriverGetsDelegationBlock(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	if rc := cmdAdd([]string{"task", "Driver"}); rc != 0 {
		t.Fatal("add task")
	}
	bindSession(t, "driver", "sid-driver")
	out := sessionStartOutput(t)
	if !strings.Contains(out, delegationMarker) {
		t.Fatalf("bound driver session should get the delegation block, got:\n%s", out)
	}
	for _, want := range []string{"flow auto", "spawning auto child:", "inline", "prod"} {
		if !strings.Contains(out, want) {
			t.Errorf("delegation block missing %q", want)
		}
	}
}

func TestSessionStartUnboundGetsNoDelegationBlock(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "")
	if out := sessionStartOutput(t); strings.Contains(out, delegationMarker) {
		t.Errorf("unbound session must not get the delegation block:\n%s", out)
	}
}

func TestSessionStartAutoRunGetsNoDelegationBlock(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	if rc := cmdAdd([]string{"task", "Child"}); rc != 0 {
		t.Fatal("add task")
	}
	seedRunningAuto(t, "child", 999999)
	t.Setenv("CLAUDE_CODE_SESSION_ID", fakeSessionID("child"))
	if out := sessionStartOutput(t); strings.Contains(out, delegationMarker) {
		t.Errorf("a running auto child must not get the delegation block (recursion):\n%s", out)
	}
}

func TestSessionStartAutoEnvGetsNoDelegationBlock(t *testing.T) {
	setupFlowRoot(t)
	if rc := cmdAdd([]string{"task", "Child"}); rc != 0 {
		t.Fatal("add task")
	}
	bindSession(t, "child", "sid-child")
	t.Setenv("FLOW_AUTO_RUN", "child")
	if out := sessionStartOutput(t); strings.Contains(out, delegationMarker) {
		t.Errorf("FLOW_AUTO_RUN session must not get the delegation block:\n%s", out)
	}
}

func TestLaunchAutoRunMarksChildEnv(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "envy")
	var gotEnv []string
	old := autoLauncher
	autoLauncher = func(slug, wd, lp, inj string, env []string) (int, error) {
		gotEnv = env
		return 7, nil
	}
	t.Cleanup(func() { autoLauncher = old })
	t.Setenv("FLOW_AUTO_RUN", "stale-parent")
	if rc := cmdDo([]string{"envy", "--auto"}); rc != 0 {
		t.Fatalf("do --auto rc=%d", rc)
	}
	n := 0
	for _, kv := range gotEnv {
		if strings.HasPrefix(kv, "FLOW_AUTO_RUN=") {
			n++
			if kv != "FLOW_AUTO_RUN=envy" {
				t.Errorf("env %q, want FLOW_AUTO_RUN=envy", kv)
			}
		}
	}
	if n != 1 {
		t.Errorf("FLOW_AUTO_RUN entries = %d, want exactly 1", n)
	}
}

func writeBrief(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFlowAutoCreatesChildAndLaunches(t *testing.T) {
	root := setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	if rc := cmdAdd([]string{"task", "Driver"}); rc != 0 {
		t.Fatal("add driver")
	}
	bindSession(t, "driver", "sid-driver")
	calls, gotSlug, _, _, _ := stubAutoLauncher(t, 5151)
	brief := "# Scan services\n\n## Done when\nlist written\n"
	bp := writeBrief(t, brief)

	var rc int
	out := captureStdout(t, func() {
		rc = cmdAuto([]string{"Scan services in EB", "--slug", "eb-scan", "--brief", bp})
	})
	if rc != 0 {
		t.Fatalf("flow auto rc=%d out=%s", rc, out)
	}
	db := openFlowDB(t)
	task, err := flowdb.GetTask(db, "eb-scan")
	if err != nil {
		t.Fatal(err)
	}
	if !task.ParentSlug.Valid || task.ParentSlug.String != "driver" {
		t.Errorf("parent = %+v, want driver", task.ParentSlug)
	}
	watches, _ := flowdb.ListWatches(db, "user/driver")
	if len(watches) != 1 || watches[0] != "eb-scan" {
		t.Errorf("watches = %v, want [eb-scan]", watches)
	}
	got, err := os.ReadFile(filepath.Join(root, "tasks", "eb-scan", "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != brief {
		t.Errorf("brief = %q, want %q", got, brief)
	}
	if *calls != 1 || *gotSlug != "eb-scan" {
		t.Errorf("launcher calls=%d slug=%q, want 1 eb-scan", *calls, *gotSlug)
	}
	if task.AutoRunStatus.String != "running" {
		t.Errorf("auto_run_status = %q, want running", task.AutoRunStatus.String)
	}
	if !strings.Contains(out, "spawning auto child: eb-scan") {
		t.Errorf("output should announce the child, got:\n%s", out)
	}
}

func TestFlowAutoAutoSlug(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	stubAutoLauncher(t, 1)
	bp := writeBrief(t, "do it\n")
	if rc := cmdAuto([]string{"Audit Variables", "--brief", bp}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	if _, err := flowdb.GetTask(openFlowDB(t), "audit-variables"); err != nil {
		t.Errorf("auto-slugged task missing: %v", err)
	}
}

func TestFlowAutoBriefFromStdin(t *testing.T) {
	root := setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	stubAutoLauncher(t, 1)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
	w.WriteString("from stdin\n")
	w.Close()
	if rc := cmdAuto([]string{"Piped", "--slug", "piped", "--brief", "-"}); rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	got, _ := os.ReadFile(filepath.Join(root, "tasks", "piped", "brief.md"))
	if string(got) != "from stdin\n" {
		t.Errorf("brief = %q", got)
	}
}

func TestFlowAutoRequiresBrief(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "")
	calls, _, _, _, _ := stubAutoLauncher(t, 1)
	empty := writeBrief(t, "  \n")
	for name, args := range map[string][]string{
		"no brief":    {"Nope", "--slug", "nope"},
		"empty brief": {"Nope", "--slug", "nope", "--brief", empty},
		"no name":     {},
	} {
		var rc int
		stderrOf(t, func() { rc = cmdAuto(args) })
		if rc != 2 {
			t.Errorf("%s: rc=%d, want 2", name, rc)
		}
	}
	if _, err := flowdb.GetTask(openFlowDB(t), "nope"); err == nil {
		t.Error("a refused flow auto must not create the task")
	}
	if *calls != 0 {
		t.Errorf("launcher called %d times, want 0", *calls)
	}
}

func TestFlowAutoRefusedInsideAutoRun(t *testing.T) {
	setupFlowRoot(t)
	t.Setenv("FLOW_AUTO_RUN", "some-child")
	calls, _, _, _, _ := stubAutoLauncher(t, 1)
	bp := writeBrief(t, "x\n")
	var rc int
	errOut := stderrOf(t, func() { rc = cmdAuto([]string{"Grandchild", "--slug", "gc", "--brief", bp}) })
	if rc == 0 {
		t.Fatal("flow auto inside an auto run must refuse (no grandchildren)")
	}
	if !strings.Contains(errOut, "grandchild") {
		t.Errorf("error should explain the no-grandchildren rule, got: %s", errOut)
	}
	if *calls != 0 {
		t.Error("launcher must not run")
	}
}

func TestAutoPromptResultContractOnlyWithParent(t *testing.T) {
	p := buildAutoBootstrapPrompt("child", "regular", "", "papa")
	for _, want := range []string{"updates/", "findings", "update file", "<=200"} {
		if !strings.Contains(p, want) {
			t.Errorf("parented auto prompt missing result contract %q:\n%s", want, p)
		}
	}
	orphan := buildAutoBootstrapPrompt("child", "regular", "", "")
	if strings.Contains(orphan, "findings") {
		t.Errorf("parentless auto prompt must not carry the result contract:\n%s", orphan)
	}
}

func TestAutoPromptForbidsGrandchildren(t *testing.T) {
	p := buildAutoBootstrapPrompt("child", "regular", "", "papa")
	if !strings.Contains(p, "flow auto") {
		t.Errorf("parented auto prompt should say not to spawn auto children:\n%s", p)
	}
}

func TestSkillDelegationReference(t *testing.T) {
	main, err := fs.ReadFile(skillFiles(), "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	core := string(main)
	if !strings.Contains(core, "references/delegation.md") {
		t.Error("SKILL.md must point at references/delegation.md")
	}
	ref, err := fs.ReadFile(skillFiles(), "references/delegation.md")
	if err != nil {
		t.Fatalf("references/delegation.md missing: %v", err)
	}
	for _, want := range []string{"flow auto", "Done when", "never block waiting for a reply", "spawning auto child:", "grandchild"} {
		if !strings.Contains(string(ref), want) {
			t.Errorf("delegation.md missing %q", want)
		}
	}
}

func TestNoEmDashInDelegationText(t *testing.T) {
	ref, _ := fs.ReadFile(skillFiles(), "references/delegation.md")
	texts := map[string]string{
		"delegation.md": string(ref),
		"hook block":    driverDelegationBlock(),
		"driver block":  driverRoleBlock(),
		"prompt":        buildAutoBootstrapPrompt("c", "regular", "", "p"),
	}
	for where, s := range texts {
		if where == "prompt" {
			// The pre-existing prompt text has em dashes; only check our addition.
			s = autoResultContract("p")
		}
		if strings.ContainsRune(s, '\u2014') {
			t.Errorf("%s contains an em dash", where)
		}
	}
}

func stderrOf(t *testing.T, f func()) string {
	t.Helper()
	orig := os.Stderr
	drain := captureStderr(t)
	f()
	out := drain()
	os.Stderr = orig
	return out
}
