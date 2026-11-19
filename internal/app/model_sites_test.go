package app

import (
	"strings"
	"testing"

	"flow/internal/flowdb"
	"flow/internal/harness"
	"flow/internal/harness/claude"
	"flow/internal/harness/codex"
)

// stubOwnerTickRunner records the launch options of every headless tick.
func stubOwnerTickRunner(t *testing.T) *[]harness.LaunchOpts {
	t.Helper()
	old := ownerTickRunner
	var got []harness.LaunchOpts
	ownerTickRunner = func(_ harness.Harness, _ string, opts harness.LaunchOpts) error {
		got = append(got, opts)
		return nil
	}
	t.Cleanup(func() { ownerTickRunner = old })
	return &got
}

func addTestOwner(t *testing.T, slug string, extra ...string) {
	t.Helper()
	args := append([]string{"owner", "Owner " + slug, "--slug", slug, "--work-dir", t.TempDir()}, extra...)
	if rc := cmdAdd(args); rc != 0 {
		t.Fatalf("add owner rc=%d", rc)
	}
}

// An owner without --model/--effort ticks on the harness default.
func TestOwnerTickNoModelUnchanged(t *testing.T) {
	setupFlowRoot(t)
	got := stubOwnerTickRunner(t)
	addTestOwner(t, "o1")
	if rc := cmdOwnerTick([]string{"o1"}); rc != 0 {
		t.Fatalf("tick rc=%d", rc)
	}
	if len(*got) != 1 || (*got)[0] != (harness.LaunchOpts{}) {
		t.Errorf("tick opts = %+v, want one empty", *got)
	}
}

// --model/--effort on add owner are stored and used by every tick;
// `owner start --model` changes only the model and later ticks use it.
func TestOwnerModelPersistsAcrossTicks(t *testing.T) {
	setupFlowRoot(t)
	got := stubOwnerTickRunner(t)
	addTestOwner(t, "o1", "--model", "haiku", "--effort", "low")

	o, err := flowdb.GetOwner(openFlowDB(t), "o1")
	if err != nil {
		t.Fatal(err)
	}
	if o.Model.String != "haiku" || o.Effort.String != "low" {
		t.Fatalf("stored model=%q effort=%q", o.Model.String, o.Effort.String)
	}
	for i := 0; i < 2; i++ {
		if rc := cmdOwnerTick([]string{"o1"}); rc != 0 {
			t.Fatalf("tick rc=%d", rc)
		}
	}
	if rc := ownerStart([]string{"o1", "--model", "sonnet"}); rc != 0 {
		t.Fatalf("start rc=%d", rc)
	}
	if rc := cmdOwnerTick([]string{"o1"}); rc != 0 {
		t.Fatalf("tick rc=%d", rc)
	}
	want := []harness.LaunchOpts{
		{Model: "haiku", Effort: "low"},
		{Model: "haiku", Effort: "low"},
		{Model: "sonnet", Effort: "low"},
	}
	if len(*got) != len(want) {
		t.Fatalf("ticks = %+v, want %+v", *got, want)
	}
	for i := range want {
		if (*got)[i] != want[i] {
			t.Errorf("tick %d opts = %+v, want %+v", i, (*got)[i], want[i])
		}
	}

	out := captureStdout(t, func() { ownerShow([]string{"o1"}) })
	if !strings.Contains(out, "model:        sonnet") || !strings.Contains(out, "effort:       low") {
		t.Errorf("owner show missing model/effort:\n%s", out)
	}
}

// Flags before the slug work for owner start too, and bad values are a
// usage error that stores nothing.
func TestOwnerStartModelFlags(t *testing.T) {
	setupFlowRoot(t)
	addTestOwner(t, "o1")
	if rc := ownerStart([]string{"--effort", "high", "o1"}); rc != 0 {
		t.Fatalf("start rc=%d", rc)
	}
	if rc := ownerStart([]string{"o1", "--model", "-x"}); rc != 2 {
		t.Errorf("bad model rc=%d, want 2", rc)
	}
	o, err := flowdb.GetOwner(openFlowDB(t), "o1")
	if err != nil {
		t.Fatal(err)
	}
	if o.Model.String != "" || o.Effort.String != "high" {
		t.Errorf("stored model=%q effort=%q, want \"\" and high", o.Model.String, o.Effort.String)
	}
	if rc := cmdAdd([]string{"owner", "Bad", "--work-dir", t.TempDir(), "--effort", "a b"}); rc != 2 {
		t.Errorf("add owner with bad effort rc=%d, want 2", rc)
	}
}

// The interactive `flow owner tick` tab carries the owner's model flags,
// and none without them.
func TestOwnerInteractiveTickModel(t *testing.T) {
	setupFlowRoot(t)
	stubNewUUID(t, "658bf2be-5ae3-4842-a8a4-e0d0b785514d")
	_, script := stubITerm(t)
	addTestOwner(t, "plain")
	addTestOwner(t, "fast", "--model", "haiku", "--effort", "low")

	if rc := ownerTickManual([]string{"plain"}); rc != 0 {
		t.Fatalf("tick rc=%d", rc)
	}
	if s := script(); strings.Contains(s, "--model") || strings.Contains(s, "--effort") {
		t.Errorf("plain owner tab must not carry model flags: %s", s)
	}
	if rc := ownerTickManual([]string{"fast"}); rc != 0 {
		t.Fatalf("tick rc=%d", rc)
	}
	if s := script(); !strings.Contains(s, "--model 'haiku' --effort 'low'") {
		t.Errorf("owner tab missing model flags: %s", s)
	}
}

// The close-out sweep runs on the task's stored model/effort, and on the
// harness default when none is stored.
func TestCloseoutSweepUsesTaskModel(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "plain")
	seedTask(t, "fast")
	db := openFlowDB(t)
	if _, err := db.Exec(`UPDATE tasks SET model='haiku', effort='low' WHERE slug='fast'`); err != nil {
		t.Fatal(err)
	}
	old := claude.SkipPermissionsRunner
	t.Cleanup(func() { claude.SkipPermissionsRunner = old })
	var got harness.LaunchOpts
	claude.SkipPermissionsRunner = func(_ string, opts harness.LaunchOpts) error { got = opts; return nil }

	for slug, want := range map[string]harness.LaunchOpts{
		"plain": {},
		"fast":  {Model: "haiku", Effort: "low"},
	} {
		task, err := flowdb.GetTask(db, slug)
		if err != nil {
			t.Fatal(err)
		}
		got = harness.LaunchOpts{Model: "unset"}
		if err := runCloseoutSweep(claude.New(), task); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: sweep opts = %+v, want %+v", slug, got, want)
		}
	}
}

// A codex bootstrap mints its session with the probe on the task's model.
func TestCodexProbeUsesTaskModel(t *testing.T) {
	setupFlowRoot(t)
	seedTask(t, "cx")
	for _, h := range allHarnesses() {
		t.Setenv(h.SessionIDEnvVar(), "")
	}
	old := codex.ProbeRunner
	t.Cleanup(func() { codex.ProbeRunner = old })
	var got harness.LaunchOpts
	codex.ProbeRunner = func(_ string, opts harness.LaunchOpts) ([]byte, error) {
		got = opts
		return []byte(`{"type":"thread.started","thread_id":"019ff4b1-6162-7263-974f-f5f1866ad0fe"}`), nil
	}
	_, _ = stubITerm(t)
	if rc := cmdDo([]string{"cx", "--harness", "codex", "--model", "gpt-5", "--effort", "high"}); rc != 0 {
		t.Fatalf("cmdDo rc=%d", rc)
	}
	if want := (harness.LaunchOpts{Model: "gpt-5", Effort: "high"}); got != want {
		t.Errorf("probe opts = %+v, want %+v", got, want)
	}
}
