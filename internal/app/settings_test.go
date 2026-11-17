package app

import (
	"strings"
	"testing"
	"time"
)

func TestConfigGetSetRoundtrip(t *testing.T) {
	initTempFlowRoot(t)

	// Default (no file): copy DISABLED — it is opt-in.
	if transcriptsCopyEnabled("any") {
		t.Fatal("default transcripts.copy should be false (opt-in)")
	}
	// set / get a bool: turn it on globally.
	if rc := cmdConfig([]string{"set", "transcripts.copy", "true"}); rc != 0 {
		t.Fatalf("config set rc=%d", rc)
	}
	if !transcriptsCopyEnabled("any") {
		t.Fatal("after set true, copy should be enabled")
	}
	out := captureStdout(t, func() { cmdConfig([]string{"get", "transcripts.copy"}) })
	if strings.TrimSpace(out) != "true" {
		t.Fatalf("config get = %q, want true", out)
	}
	// per-task override wins: disable it for one task while the global is on.
	if rc := cmdConfig([]string{"set", "transcripts.tasks.special.copy", "false"}); rc != 0 {
		t.Fatalf("config set rc=%d", rc)
	}
	if transcriptsCopyEnabled("special") {
		t.Fatal("per-task override should disable copy for that task")
	}
	if !transcriptsCopyEnabled("other") {
		t.Fatal("non-overridden task should follow the global (on)")
	}
	// int knob.
	if rc := cmdConfig([]string{"set", "transcripts.max_tool_result_bytes", "2048"}); rc != 0 {
		t.Fatalf("config set rc=%d", rc)
	}
	if got := transcriptsMaxToolResultBytes(); got != 2048 {
		t.Fatalf("max_tool_result_bytes = %d, want 2048", got)
	}
}

func TestConfigAcceptsOnOffSpelling(t *testing.T) {
	initTempFlowRoot(t)
	// `off` must actually disable capture (docs/skill tell users this form).
	if rc := cmdConfig([]string{"set", "transcripts.copy", "off"}); rc != 0 {
		t.Fatalf("set off rc=%d", rc)
	}
	if transcriptsCopyEnabled("t") {
		t.Fatal("transcripts.copy=off did not disable capture")
	}
	if rc := cmdConfig([]string{"set", "transcripts.tasks.t.copy", "on"}); rc != 0 {
		t.Fatalf("set on rc=%d", rc)
	}
	if !transcriptsCopyEnabled("t") {
		t.Fatal("per-task copy=on did not re-enable capture")
	}
}

func TestConfigGetUnknownKey(t *testing.T) {
	initTempFlowRoot(t)
	if rc := cmdConfig([]string{"get", "nope.not.here"}); rc != 1 {
		t.Fatalf("get unknown key rc=%d, want 1", rc)
	}
}

// TestConfigGetShowsDefaults pins that a documented key returns its default
// (marked) when unset, instead of erroring — the behaviour the base
// regressed on. A set value still prints raw (no marker).
func TestConfigGetShowsDefaults(t *testing.T) {
	initTempFlowRoot(t)
	cases := map[string]string{
		"transcripts.copy":                  "false (default)",
		"transcripts.max_tool_result_bytes": "16384 (default)",
		"transcripts.retention_days":        "0 (default)",
		"nudge.notes.enabled":               "true (default)",
		"nudge.notes.base_turns":            "15 (default)",
		"nudge.notes.step":                  "0.2 (default)",
	}
	for key, want := range cases {
		out := captureStdout(t, func() {
			if rc := cmdConfig([]string{"get", key}); rc != 0 {
				t.Fatalf("get %s rc=%d, want 0", key, rc)
			}
		})
		if strings.TrimSpace(out) != want {
			t.Errorf("get %s = %q, want %q", key, strings.TrimSpace(out), want)
		}
	}
	// Once set, the raw value prints (no default marker).
	if rc := cmdConfig([]string{"set", "nudge.notes.base_turns", "20"}); rc != 0 {
		t.Fatalf("set rc=%d", rc)
	}
	out := captureStdout(t, func() { cmdConfig([]string{"get", "nudge.notes.base_turns"}) })
	if strings.TrimSpace(out) != "20" {
		t.Errorf("get after set = %q, want 20", strings.TrimSpace(out))
	}
}

// TestConfigListShowsDefaults pins that `config list` enumerates every
// documented key with its default (marked) even with no settings file, and
// reflects both overrides and extra stored keys.
func TestConfigListShowsDefaults(t *testing.T) {
	initTempFlowRoot(t)
	// Empty file: still lists all known keys, each marked default.
	out := captureStdout(t, func() {
		if rc := cmdConfig([]string{"list"}); rc != 0 {
			t.Fatalf("list rc=%d", rc)
		}
	})
	for _, want := range []string{
		"transcripts.copy = false (default)",
		"transcripts.max_tool_result_bytes = 16384 (default)",
		"transcripts.retention_days = 0 (default)",
		"nudge.notes.enabled = true (default)",
		"nudge.notes.base_turns = 15 (default)",
		"nudge.notes.step = 0.2 (default)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list missing %q; got:\n%s", want, out)
		}
	}
	// Set one global and one per-task override; both must show, the global
	// without a default marker and the per-task as an extra key.
	cmdConfig([]string{"set", "nudge.notes.enabled", "off"})
	cmdConfig([]string{"set", "transcripts.tasks.special.copy", "off"})
	out = captureStdout(t, func() { cmdConfig([]string{"list"}) })
	if !strings.Contains(out, "nudge.notes.enabled = false\n") {
		t.Errorf("list should show the set global without a default marker; got:\n%s", out)
	}
	if strings.Contains(out, "nudge.notes.enabled = false (default)") {
		t.Errorf("a set key must not be marked default; got:\n%s", out)
	}
	if !strings.Contains(out, "transcripts.tasks.special.copy = false") {
		t.Errorf("list should show the per-task override; got:\n%s", out)
	}
}

// TestCaptureLatency measures the typical (incremental, one turn) and
// worst-case (full rebuild of a large transcript) capture cost. The Stop
// hook must stay fast; this both guards that and prints the numbers the
// task note records.
func TestCaptureLatency(t *testing.T) {
	initTempFlowRoot(t)
	enableTranscriptCopy(t)
	wd := t.TempDir()
	task := transcriptTask("lat", wd, "sid-lat")

	// Build a large transcript: ~2000 turns.
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		b.WriteString(line1)
		b.WriteString(line2)
	}
	big := b.String()
	writeClaudeJSONL(t, wd, "sid-lat", big)

	// Worst case: cold rebuild of the whole file.
	start := time.Now()
	if _, _, err := captureTranscript(task); err != nil {
		t.Fatal(err)
	}
	rebuild := time.Since(start)

	// Typical: one more turn appended, incremental read of just the delta.
	writeClaudeJSONL(t, wd, "sid-lat", big+line1+line2)
	start = time.Now()
	if _, _, err := captureTranscript(task); err != nil {
		t.Fatal(err)
	}
	incremental := time.Since(start)

	t.Logf("capture latency: rebuild(4000 records)=%v  incremental(1 turn)=%v", rebuild, incremental)
	// The incremental path is what the Stop hook pays every turn; keep it
	// comfortably under a hook budget even on CI.
	if incremental > 50*time.Millisecond {
		t.Errorf("incremental capture too slow: %v (>50ms)", incremental)
	}
}
