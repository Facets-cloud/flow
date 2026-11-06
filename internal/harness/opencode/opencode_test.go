package opencode

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"flow/internal/harness"
	"flow/internal/spawner"
)

const testModel = "opencode-go/deepseek-v4.1-flash"

func sidOf(ch string) string { return "ses_" + strings.Repeat(ch, 26) }

func TestNewSessionID(t *testing.T) {
	orig := RandReader
	t.Cleanup(func() { RandReader = orig })
	RandReader = bytes.NewReader(bytes.Repeat([]byte{0}, 128))

	h := New()
	id, err := h.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "ses_") || len(id) != 30 {
		t.Errorf("NewSessionID=%q, want ses_ + 26 chars", id)
	}
	if err := h.ValidateSessionID(id); err != nil {
		t.Errorf("minted id fails validation: %v", err)
	}

	RandReader = errReader{}
	if _, err := h.NewSessionID(); err == nil {
		t.Error("NewSessionID swallowed reader error")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

func TestValidateSessionID(t *testing.T) {
	h := New()
	valid := sidOf("A")
	if err := h.ValidateSessionID(valid); err != nil {
		t.Errorf("valid id rejected: %v", err)
	}
	invalid := []string{
		"ses",
		"abc",
		"658bf2be-5ae3-4842-a8a4-e0d0b785514d",
		"ses_" + strings.Repeat("a", 25),
		"ses_" + strings.Repeat("a", 25) + "-",
	}
	for _, s := range invalid {
		if err := h.ValidateSessionID(s); err == nil {
			t.Errorf("ValidateSessionID(%q) accepted invalid id", s)
		}
	}
}

func TestLaunchCmd(t *testing.T) {
	h := New()
	sid := sidOf("A")
	for _, tc := range []struct {
		name string
		opts harness.LaunchOpts
	}{
		{"plain", harness.LaunchOpts{}},
		{"skip", harness.LaunchOpts{SkipPermissions: true}},
		{"inject", harness.LaunchOpts{Inject: "follow up"}},
		{"skip+inject", harness.LaunchOpts{SkipPermissions: true, Inject: "follow up"}},
	} {
		for _, hasModel := range []bool{false, true} {
			t.Run(tc.name, func(t *testing.T) {
				if hasModel {
					t.Setenv("FLOW_OPENCODE_MODEL", testModel)
				} else {
					t.Setenv("FLOW_OPENCODE_MODEL", "")
				}
				prompt := "work"
				if tc.opts.Inject != "" {
					prompt += "\n\n" + harness.InjectionMarker + "\n" + tc.opts.Inject
				}
				want := "opencode -s " + sid
				if tc.opts.SkipPermissions {
					want += " --auto"
				}
				want += " --prompt " + spawner.ShellQuote(prompt)
				if got := h.LaunchCmd(sid, "work", tc.opts); got != want {
					t.Errorf("LaunchCmd=%q, want %q", got, want)
				}
			})
		}
	}

	// Single-quote escaping is POSIX: close, escaped quote, reopen.
	want := "opencode -s " + sid + ` --prompt 'it'\''s fine'`
	if got := h.LaunchCmd(sid, "it's fine", harness.LaunchOpts{}); got != want {
		t.Errorf("LaunchCmd quote=%q, want %q", got, want)
	}
}

func TestResumeCmd(t *testing.T) {
	h := New()
	sid := sidOf("A")
	for _, tc := range []struct {
		name string
		opts harness.LaunchOpts
	}{
		{"plain", harness.LaunchOpts{}},
		{"skip", harness.LaunchOpts{SkipPermissions: true}},
		{"inject", harness.LaunchOpts{Inject: "follow up"}},
		{"skip+inject", harness.LaunchOpts{SkipPermissions: true, Inject: "follow up"}},
	} {
		for _, hasModel := range []bool{false, true} {
			t.Run(tc.name, func(t *testing.T) {
				if hasModel {
					t.Setenv("FLOW_OPENCODE_MODEL", testModel)
				} else {
					t.Setenv("FLOW_OPENCODE_MODEL", "")
				}
				want := "opencode -s " + sid
				if tc.opts.SkipPermissions {
					want += " --auto"
				}
				if tc.opts.Inject != "" {
					want += " --prompt " + spawner.ShellQuote(harness.InjectionMarker+"\n"+tc.opts.Inject)
				}
				if got := h.ResumeCmd(sid, tc.opts); got != want {
					t.Errorf("ResumeCmd=%q, want %q", got, want)
				}
			})
		}
	}
}

func TestAutoRunArgv(t *testing.T) {
	h := New()
	sid := sidOf("A")
	for _, tc := range []struct {
		name     string
		opts     harness.LaunchOpts
		hasModel bool
	}{
		{"plain", harness.LaunchOpts{}, false},
		{"skip", harness.LaunchOpts{SkipPermissions: true}, false},
		{"inject", harness.LaunchOpts{Inject: "follow up"}, false},
		{"skip+inject+model", harness.LaunchOpts{SkipPermissions: true, Inject: "follow up"}, true},
		{"model", harness.LaunchOpts{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.hasModel {
				t.Setenv("FLOW_OPENCODE_MODEL", testModel)
			} else {
				t.Setenv("FLOW_OPENCODE_MODEL", "")
			}
			prompt := "work"
			if tc.opts.Inject != "" {
				prompt += "\n\n" + harness.InjectionMarker + "\n" + tc.opts.Inject
			}
			want := []string{"opencode", "run", "-s", sid}
			if tc.opts.SkipPermissions {
				want = append(want, "--auto")
			}
			if tc.hasModel {
				want = append(want, "-m", testModel)
			}
			want = append(want, prompt)
			got := h.AutoRunArgv(sid, "work", tc.opts)
			if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Errorf("AutoRunArgv=%q, want %q", got, want)
			}
		})
	}
}

func TestSkipPermissionsRun(t *testing.T) {
	orig := RunCmd
	t.Cleanup(func() { RunCmd = orig })
	var got []string
	RunCmd = func(argv []string) error {
		got = append([]string(nil), argv...)
		return nil
	}
	h := New()

	t.Setenv("FLOW_OPENCODE_MODEL", "")
	if err := h.SkipPermissionsRun("close out"); err != nil {
		t.Fatal(err)
	}
	want := []string{"opencode", "run", "--auto", "close out"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv=%q, want %q", got, want)
	}

	t.Setenv("FLOW_OPENCODE_MODEL", testModel)
	if err := h.SkipPermissionsRun("close out"); err != nil {
		t.Fatal(err)
	}
	want = []string{"opencode", "run", "--auto", "-m", testModel, "close out"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv=%q, want %q", got, want)
	}
}

func TestLiveSessionIDs(t *testing.T) {
	orig := PSRunner
	t.Cleanup(func() { PSRunner = orig })

	x, y, z := sidOf("A"), sidOf("b"), sidOf("C")
	PSRunner = func() ([]byte, error) {
		return []byte(strings.Join([]string{
			"100 opencode -s " + x + " --prompt 'x'",
			"101 /Users/a/.opencode/bin/opencode run --session=" + y + " --prompt y",
			"102 timeout 1800 opencode run -s " + z + " --prompt z",
			"103 claude --session-id 658bf2be-5ae3-4842-a8a4-e0d0b785514d",
			"104 opencode run -s " + y + " -s " + y,
			"105 opencode -s " + x + " --prompt 'x'",
		}, "\n")), nil
	}

	live, err := New().LiveSessionIDs()
	if err != nil {
		t.Fatal(err)
	}
	if live[x] != 2 {
		t.Errorf("live[%s]=%d, want 2", x, live[x])
	}
	if live[y] != 2 {
		t.Errorf("live[%s]=%d, want 2 (one per line)", y, live[y])
	}
	if _, ok := live[z]; ok {
		t.Errorf("timeout-wrapped line counted as live: %v", live)
	}
	if live[strings.ToLower(x)] != 0 {
		t.Errorf("ids must stay case-sensitive: %v", live)
	}
	if len(live) != 2 {
		t.Errorf("live=%v, want exactly the two opencode ids", live)
	}

	PSRunner = func() ([]byte, error) { return nil, errors.New("ps exploded") }
	if _, err := New().LiveSessionIDs(); err == nil {
		t.Error("LiveSessionIDs swallowed ps error")
	}
}

func TestInstallSessionStartHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	h := New()
	path := filepath.Join(home, ".config", "opencode", "plugins", "flow", "index.js")

	added, err := h.InstallSessionStartHook("ignored")
	if err != nil || !added {
		t.Fatalf("first install added=%v err=%v", added, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, pluginJS) {
		t.Errorf("plugin bytes = %q, want %q", raw, pluginJS)
	}

	if added, err := h.InstallSessionStartHook("ignored"); err != nil || added {
		t.Fatalf("idempotent install added=%v err=%v", added, err)
	}

	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if added, err := h.InstallSessionStartHook("ignored"); err != nil || !added {
		t.Fatalf("modified file should reinstall: added=%v err=%v", added, err)
	}

	removed, err := h.UninstallSessionStartHook("ignored")
	if err != nil || !removed {
		t.Fatalf("uninstall removed=%v err=%v", removed, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("plugin still present after uninstall: %v", err)
	}
	if removed, err := h.UninstallSessionStartHook("ignored"); err != nil || removed {
		t.Fatalf("second uninstall removed=%v err=%v", removed, err)
	}
}

func TestSkillInstallAndUninstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	h := New()

	files := fstest.MapFS{
		"SKILL.md":        &fstest.MapFile{Data: []byte("# flow skill\n")},
		"references/a.md": &fstest.MapFile{Data: []byte("reference\n")},
	}
	if err := h.InstallSkill(files); err != nil {
		t.Fatalf("InstallSkill: %v", err)
	}
	base := filepath.Join(home, ".config", "opencode", "skills", "flow")
	for _, rel := range []string{"SKILL.md", filepath.Join("references", "a.md")} {
		if _, err := os.Stat(filepath.Join(base, rel)); err != nil {
			t.Errorf("expected %s: %v", rel, err)
		}
	}

	skillPath, err := h.SkillInstallPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "SKILL.md"); skillPath != want {
		t.Errorf("SkillInstallPath=%q, want %q", skillPath, want)
	}
	versionPath, err := h.SkillVersionPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "VERSION"); versionPath != want {
		t.Errorf("SkillVersionPath=%q, want %q", versionPath, want)
	}

	if err := h.UninstallSkill(); err != nil {
		t.Fatalf("UninstallSkill: %v", err)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Errorf("skill dir still present: %v", err)
	}
	if err := h.UninstallSkill(); err != nil {
		t.Errorf("second UninstallSkill: %v", err)
	}
}

func TestPrepareSession(t *testing.T) {
	orig := APIRunner
	t.Cleanup(func() { APIRunner = orig })

	var calls [][]string
	var getResp string
	APIRunner = func(args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		switch args[0] {
		case "session.get":
			return []byte(getResp), nil
		default:
			return []byte(`{}`), nil
		}
	}

	sid := sidOf("A")
	workDir := "/tmp/work"
	meta := harness.SessionMeta{
		TaskSlug: "task-x",
		FlowRoot: "/tmp/flow-root",
		FlowBin:  "/usr/local/bin/flow",
		Auto:     true,
	}
	h := &opencode{}

	t.Run("create and merge", func(t *testing.T) {
		calls = nil
		getResp = `{"data":{"metadata":{"unrelated":"keep"}}}`
		t.Setenv("FLOW_OPENCODE_MODEL", "")
		if err := h.PrepareSession(workDir, sid, meta); err != nil {
			t.Fatalf("PrepareSession: %v", err)
		}
		if len(calls) != 3 {
			t.Fatalf("calls=%v, want create+get+update", calls)
		}

		if calls[0][0] != "session.create" || calls[1][0] != "session.get" || calls[2][0] != "session.update" {
			t.Fatalf("call order=%v", calls)
		}
		body := decodeJSON(t, calls[0][2])
		if body["id"] != sid {
			t.Errorf("create id=%v, want %s", body["id"], sid)
		}
		if body["title"] != meta.TaskSlug {
			t.Errorf("create title=%v, want %s", body["title"], meta.TaskSlug)
		}
		loc := mustMap(t, body["location"])
		if loc["directory"] != workDir {
			t.Errorf("location.directory=%v, want %s", loc["directory"], workDir)
		}
		flow := mustMap(t, mustMap(t, body["metadata"])["flow"])
		if flow["taskSlug"] != meta.TaskSlug || flow["flowRoot"] != meta.FlowRoot ||
			flow["flowBin"] != meta.FlowBin || flow["auto"] != true {
			t.Errorf("metadata.flow=%v, want %+v", flow, meta)
		}
		if _, ok := body["model"]; ok {
			t.Errorf("model present with FLOW_OPENCODE_MODEL unset: %v", body["model"])
		}

		if calls[1][1] != "--param" || calls[1][2] != "sessionID="+sid {
			t.Errorf("get args=%v", calls[1])
		}
		upd := decodeJSON(t, calls[2][4])
		merged := mustMap(t, upd["metadata"])
		if merged["unrelated"] != "keep" {
			t.Errorf("update dropped unrelated metadata: %v", merged)
		}
		if mustMap(t, merged["flow"])["taskSlug"] != meta.TaskSlug {
			t.Errorf("update flow metadata=%v", merged["flow"])
		}
	})

	t.Run("model with variant", func(t *testing.T) {
		calls = nil
		getResp = `{"metadata":null}`
		t.Setenv("FLOW_OPENCODE_MODEL", testModel+"#fast")
		if err := h.PrepareSession(workDir, sid, meta); err != nil {
			t.Fatalf("PrepareSession: %v", err)
		}
		body := decodeJSON(t, calls[0][2])
		m := mustMap(t, body["model"])
		if m["providerID"] != "opencode-go" || m["id"] != "deepseek-v4.1-flash" {
			t.Errorf("model=%v, want providerID=opencode-go id=deepseek-v4.1-flash", m)
		}
		upd := decodeJSON(t, calls[2][4])
		merged := mustMap(t, upd["metadata"])
		if _, ok := merged["unrelated"]; ok {
			t.Errorf("null metadata should not resurrect unrelated key: %v", merged)
		}
		if mustMap(t, merged["flow"])["taskSlug"] != meta.TaskSlug {
			t.Errorf("update flow metadata=%v", merged["flow"])
		}
	})

	t.Run("model without slash errors", func(t *testing.T) {
		calls = nil
		t.Setenv("FLOW_OPENCODE_MODEL", "noslash")
		err := h.PrepareSession(workDir, sid, meta)
		if err == nil || !strings.Contains(err.Error(), "FLOW_OPENCODE_MODEL") {
			t.Fatalf("error=%v, want FLOW_OPENCODE_MODEL mention", err)
		}
		if len(calls) != 0 {
			t.Errorf("api called before model validation: %v", calls)
		}
	})

	t.Run("create error propagates", func(t *testing.T) {
		t.Setenv("FLOW_OPENCODE_MODEL", "")
		APIRunner = func(args ...string) ([]byte, error) {
			return nil, errors.New("server down")
		}
		err := h.PrepareSession(workDir, sid, meta)
		if err == nil || !strings.Contains(err.Error(), "session.create") || !strings.Contains(err.Error(), "server down") {
			t.Fatalf("error=%v, want create failure wrapped", err)
		}
	})
}

func decodeJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return m
}

func mustMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("value %v (%T) is not an object", v, v)
	}
	return m
}
