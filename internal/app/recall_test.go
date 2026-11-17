package app

import (
	"encoding/json"
	"testing"
)

// TestRecallForms exercises every user-facing `flow recall` shape against a
// synthetic flow root: bound task, explicit slug, free text, --prompt, --text,
// unknown-slug-as-text, --json, and the error cases. flow.db must never be
// written.
func TestRecallForms(t *testing.T) {
	dbPath, _ := matchFixture(t)
	before := fileSum(t, dbPath)

	// Bind this process to grafana-dash so the no-arg (bound) form resolves.
	t.Setenv("CLAUDE_CODE_SESSION_ID", fakeSessionID("grafana-dash"))

	for _, args := range [][]string{
		nil,                                       // bound task (reverse lookup)
		{"grafana-dash", "--k", "2"},              // explicit slug
		{"grafana-dash", "--min", "0.5"},          // slug + confidence cut
		{"grafana-dash", "--min=0"},               // =form
		{"grafana-dash", "--json"},                // json schema
		{"grafana-dash", "--all"},                 // rank every task
		{"terraform module provider state drift"}, // multi-word → free text
		{"kafka lag on the broker partition"},     // free text
		{"grafana-dash", "--text"},                // force text on a known slug
		{"no-such-topic-xyz"},                     // unknown single token → free text
		{"grafana-dash", "--prompt", "latency panel thresholds on the dashboard", "--k", "3"},
		{"--prompt", "kafka broker partition rebalance group offsets"}, // bound + prompt
		{"go"}, // too generic as text → abstains, still rc 0
	} {
		if rc := cmdRecall(args); rc != 0 {
			t.Fatalf("recall %v rc=%d", args, rc)
		}
	}

	// --json emits the stable schema with the requested fields.
	out := captureStdout(t, func() {
		if rc := cmdRecall([]string{"grafana-dash", "--k", "2", "--min", "0", "--json"}); rc != 0 {
			t.Fatalf("json rc=%d", rc)
		}
	})
	var rj recallJSON
	if err := json.Unmarshal([]byte(out), &rj); err != nil {
		t.Fatalf("recall --json: %v\n%s", err, out)
	}
	if rj.Scorer == "" || rj.Mode == "" {
		t.Fatalf("recall --json missing scorer/mode: %+v", rj)
	}
	for _, m := range rj.Matches {
		if m.Slug == "" {
			t.Fatalf("recall --json match missing slug: %+v", m)
		}
	}

	for _, c := range []struct {
		args []string
		rc   int
	}{
		{[]string{"--prompt", "latency", "no-such-task"}, 2}, // --prompt needs a known slug or none
		{[]string{"--k", "0"}, 2},                            // nonsensical k
	} {
		if rc := cmdRecall(c.args); rc != c.rc {
			t.Errorf("recall %v: rc=%d want %d", c.args, rc, c.rc)
		}
	}

	if after := fileSum(t, dbPath); after != before {
		t.Fatal("flow recall must never write flow.db")
	}
}

// TestRecallFreeTextExcludesBound checks that in a BOUND session, free-text
// recall never surfaces the task the session is sitting in (the bug: its own
// prompts are indexed, so a free-text query can rank it #1). --include-self
// restores it. An UNBOUND session excludes nothing.
func TestRecallFreeTextExcludesBound(t *testing.T) {
	matchFixture(t)
	// Warm the index so the cold-build "building index…" notice (stderr) can't
	// bleed into the captured JSON (captureStdout merges stdout+stderr).
	if rc := cmdRecall([]string{"train"}); rc != 0 {
		t.Fatalf("recall train rc=%d", rc)
	}

	topicOf := func(args []string) recallJSON {
		t.Helper()
		var rj recallJSON
		out := captureStdout(t, func() {
			if rc := cmdRecall(args); rc != 0 {
				t.Fatalf("recall %v rc != 0", args)
			}
		})
		if err := json.Unmarshal([]byte(out), &rj); err != nil {
			t.Fatalf("recall %v --json: %v\n%s", args, err, out)
		}
		return rj
	}
	has := func(rj recallJSON, slug string) bool {
		for _, m := range rj.Matches {
			if m.Slug == slug {
				return true
			}
		}
		return false
	}

	// A free-text query that matches the grafana topic strongly.
	query := "grafana dashboard latency panel thresholds"

	// Unbound: grafana-dash is a legitimate candidate and should surface.
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	if un := topicOf([]string{query, "--k", "10", "--min", "0", "--json"}); !has(un, "grafana-dash") {
		t.Fatalf("unbound free-text recall should surface grafana-dash: %+v", un.Matches)
	}

	// Bound to grafana-dash: it must be excluded by default…
	t.Setenv("CLAUDE_CODE_SESSION_ID", fakeSessionID("grafana-dash"))
	if b := topicOf([]string{query, "--k", "10", "--min", "0", "--json"}); has(b, "grafana-dash") {
		t.Fatalf("bound free-text recall surfaced the bound task itself: %+v", b.Matches)
	}
	// …and restored by --include-self.
	if s := topicOf([]string{query, "--k", "10", "--min", "0", "--include-self", "--json"}); !has(s, "grafana-dash") {
		t.Fatalf("--include-self should restore the bound task: %+v", s.Matches)
	}
}

// TestRecallTrainAndCalibrateRefusal checks that `recall train` builds the
// index and that `--calibrate` refuses (rc 0, no calib file) on a tiny history.
func TestRecallTrainAndCalibrateRefusal(t *testing.T) {
	matchFixture(t)

	if rc := cmdRecall([]string{"train"}); rc != 0 {
		t.Fatalf("recall train rc=%d", rc)
	}
	cachePath, err := lookbackCachePath()
	if err != nil || !matchFileExists(cachePath) {
		t.Fatalf("recall train did not write the cache (%v)", err)
	}

	if rc := cmdRecall([]string{"train", "--calibrate"}); rc != 0 {
		t.Fatalf("recall train --calibrate rc=%d (should still succeed, just skip calibration)", rc)
	}
	calibPath, _ := recallCalibPath()
	if matchFileExists(calibPath) {
		t.Fatal("calibration must be refused on tiny data — no calib file should be written")
	}
	if loadRecallCalib() != nil {
		t.Fatal("loadRecallCalib must be nil when no calibration is persisted")
	}
}
