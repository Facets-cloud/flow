//go:build recalleval

package match

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestFamilyKey(t *testing.T) {
	cases := map[string]string{
		"foo-bar":                      "foo-bar",
		"foo-bar-2":                    "foo-bar",
		"foo-bar-v3":                   "foo-bar",
		"foo-bar-followup":             "foo-bar",
		"payments-uat-migrate-2":       "payments-uat-migrate",
		"retro-mig2-dbprod-cutover-11": "retro-mig2",
		"retro-mig2-anything":          "retro-mig2",
		"retro-x":                      "retro-x", // too short for the prefix rule: own stem
		"ab-2":                         "",        // stem "ab" is too generic
		"widget":                       "widget",
	}
	for in, want := range cases {
		if got := FamilyKey(in); got != want {
			t.Errorf("FamilyKey(%q) = %q, want %q", in, got, want)
		}
	}
	if FamilyKey("foo-bar") != FamilyKey("foo-bar-2") {
		t.Fatal("foo-bar and foo-bar-2 must share a family")
	}
}

func TestSlugMatcherFind(t *testing.T) {
	sm := NewSlugMatcher([]string{"foo-bar", "foo-bar-2", "widget", "ledger-migrate-dryrun"})
	cases := []struct {
		text string
		want []string
	}{
		{"see foo-bar.", []string{"foo-bar"}},
		{"see `foo-bar-2` and FOO-BAR", []string{"foo-bar", "foo-bar-2"}},
		{"~/.flow/tasks/ledger-migrate-dryrun/brief.md", []string{"ledger-migrate-dryrun"}},
		{"pre-foo-bar is another id", nil},     // inside a longer identifier
		{"foo-bar-baz is another id", nil},     // prefix of a longer identifier
		{"foo bar written loosely", nil},       // strict: hyphen form only
		{"please widget me", nil},              // single-word slugs never match
		{"foo_bar-x and xfoo-bar", nil},        // boundaries
		{"foo-bar-2-extra", nil},               // longer id
		{"(foo-bar-2)", []string{"foo-bar-2"}}, // longest wins, no foo-bar
	}
	for _, c := range cases {
		got := sortedKeys(sm.Find(c.text))
		if len(got) == 0 {
			got = nil
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Find(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestSlugMatcherStrip(t *testing.T) {
	sm := NewSlugMatcher([]string{"payments-uat-dms", "foo-bar", "gizmo"})
	in := "Follow up on payments-uat-dms, Payments UAT DMS and payments_uat_dms; also foo-bar-2 and gizmo."
	out, n := sm.Strip(in)
	if n != 4 {
		t.Fatalf("stripped %d spans, want 4: %q", n, out)
	}
	for _, bad := range []string{"payments", "uat", "dms", "foo"} {
		if strings.Contains(strings.ToLower(out), bad) {
			t.Errorf("stripped text still contains %q: %q", bad, out)
		}
	}
	if !strings.Contains(out, "gizmo") {
		t.Errorf("single-word slug must not be stripped: %q", out)
	}
	if len(sm.Find(out)) != 0 {
		t.Errorf("residual mentions after strip: %v", sm.Find(out))
	}
	// No slug: unchanged.
	if s, n := sm.Strip("nothing to see"); n != 0 || s != "nothing to see" {
		t.Fatalf("Strip changed clean text: %q %d", s, n)
	}
}

func ts(day int) string { return fmt.Sprintf("2026-01-%02dT10:00:00Z", day) }

func TestDeriveTruth(t *testing.T) {
	tasks := []TaskData{
		{Slug: "alpha-root", CreatedAt: ts(1), Project: "p1", Tags: []string{"slack", "kafka"}},
		{Slug: "alpha-child", CreatedAt: ts(2), Parent: "alpha-root"},
		{Slug: "alpha-child-2", CreatedAt: ts(3), Parent: "alpha-root"},
		{Slug: "beta-task", CreatedAt: ts(4), Project: "p1", Tags: []string{"slack"}},
		{Slug: "gamma-run", CreatedAt: ts(5), Kind: "playbook_run", Playbook: "pb"},
		{Slug: "gamma-run-b", CreatedAt: ts(6), Kind: "playbook_run", Playbook: "pb", Tags: []string{"kafka"}},
		{Slug: "delta-new", CreatedAt: ts(7), Brief: "builds on beta-task; see also epsilon-late"},
		{Slug: "epsilon-late", CreatedAt: ts(8), Updates: []Update{{Date: "2026-01-09", Text: "followed up from delta-new"}}},
		{Slug: "zeta-z", CreatedAt: ts(9), Brief: "mentions alpha-root"},
	}
	sm := NewSlugMatcher([]string{"alpha-root", "alpha-child", "alpha-child-2", "beta-task", "gamma-run", "gamma-run-b", "delta-new", "epsilon-late", "zeta-z"})
	generic := map[string]bool{"slack": true}
	truth := DeriveTruth(tasks, sm, generic)

	want := map[string]TruthSet{
		"alpha-child":   {SrcParent: {"alpha-root"}},
		"alpha-child-2": {SrcParent: {"alpha-child", "alpha-root"}, SrcFamily: {"alpha-child"}},
		"beta-task":     {SrcProject: {"alpha-root"}},                         // shared tag "slack" is generic
		"gamma-run-b":   {SrcPlaybook: {"gamma-run"}, SrcTag: {"alpha-root"}}, // "-b" is no variant suffix
		"delta-new":     {SrcMention: {"beta-task"}},                          // epsilon-late is newer: not a candidate
		"epsilon-late":  {SrcMention: {"delta-new"}},                          // its own (later) note names delta-new
		"zeta-z":        {SrcMention: {"alpha-root"}},
	}
	if !reflect.DeepEqual(truth, want) {
		t.Fatalf("truth mismatch\n got %v\nwant %v", truth, want)
	}
	for q, tsx := range truth {
		var qc string
		for _, x := range tasks {
			if x.Slug == q {
				qc = x.CreatedAt
			}
		}
		for _, c := range tsx.Union() {
			for _, x := range tasks {
				if x.Slug == c && !(x.CreatedAt < qc) {
					t.Errorf("truth %s -> %s is not older", q, c)
				}
			}
		}
	}
}

func TestDeriveTruthHubGuardAndGenericTags(t *testing.T) {
	// "watcher-hub" lists every task in its notes: a hub, so none of its
	// mention edges count.
	var tasks []TaskData
	var slugs []string
	var notes []string
	tasks = append(tasks, TaskData{Slug: "watcher-hub", CreatedAt: "2026-01-01T00:00:00Z"})
	for i := 0; i < 25; i++ {
		s := fmt.Sprintf("job-%c-x", 'a'+i)
		slugs = append(slugs, s)
		notes = append(notes, s)
		tasks = append(tasks, TaskData{Slug: s, CreatedAt: fmt.Sprintf("2026-01-01T%02d:00:00Z", 1+i), Tags: []string{"common"}})
	}
	tasks[0].Updates = []Update{{Text: strings.Join(notes, " ")}}
	tasks[1].Brief = "relates to watcher-hub"
	sm := NewSlugMatcher(append(slugs, "watcher-hub"))
	generic := GenericTags(tasks, nil) // "common" is on 25/26 tasks
	if !generic["common"] {
		t.Fatal("a tag on most tasks must be generic")
	}
	truth := DeriveTruth(tasks, sm, generic)
	for q, ts := range truth {
		if len(ts[SrcMention]) > 0 {
			t.Errorf("%s: hub mention leaked into truth: %v", q, ts[SrcMention])
		}
		if len(ts[SrcTag]) > 0 {
			t.Errorf("%s: generic tag produced truth", q)
		}
	}
	st := MentionStats(tasks, sm)
	if st[1].Pairs == 0 || st[2].Pairs != 0 || !reflect.DeepEqual(st[2].Hubs, []string{"watcher-hub"}) {
		t.Fatalf("mention stats = %+v", st)
	}
}

func TestNotesUpTo(t *testing.T) {
	ups := []Update{{Date: "2026-01-01"}, {Date: "2026-01-05"}, {Date: ""}, {Date: "2026-01-06"}}
	got := NotesUpTo(ups, "2026-01-05")
	if len(got) != 3 || got[0].Date != "2026-01-01" || got[1].Date != "2026-01-05" || got[2].Date != "" {
		t.Fatalf("NotesUpTo = %+v", got)
	}
}

func TestMetricsOf(t *testing.T) {
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	// truth {a,b,c} at ranks 2, 7, 30.
	m := metricsOf(map[string]int{"a": 2, "b": 7, "c": 30}, []string{"a", "b", "c"})
	if !near(m.R5, 1.0/3) || !near(m.R10, 2.0/3) || !near(m.R20, 2.0/3) || !near(m.MRR, 0.5) || m.Hit5 != 1 {
		t.Fatalf("metrics = %+v", m)
	}
	dcg := 1/math.Log2(3) + 1/math.Log2(8)
	idcg := 1 + 1/math.Log2(3) + 1/math.Log2(4)
	if !near(m.NDCG10, dcg/idcg) {
		t.Fatalf("ndcg = %v want %v", m.NDCG10, dcg/idcg)
	}
	// unranked truth item counts as a miss; perfect ranking gives 1s.
	m = metricsOf(map[string]int{"a": 1}, []string{"a", "z"})
	if !near(m.R10, 0.5) || !near(m.MRR, 1) || !near(m.NDCG10, 1/(1+1/math.Log2(3))) {
		t.Fatalf("metrics = %+v", m)
	}
	m = metricsOf(map[string]int{"a": 6}, []string{"a"})
	if m.Hit5 != 0 || !near(m.MRR, 1.0/6) || m.R5 != 0 || m.R10 != 1 {
		t.Fatalf("metrics = %+v", m)
	}
}

func TestBootstrapCIDeterministicAndBracketsMean(t *testing.T) {
	mk := func() *LBRow {
		r := &LBRow{}
		for i := 0; i < 50; i++ {
			r.perQuery = append(r.perQuery, QueryMetrics{MRR: float64(i%5) / 4, R10: float64(i % 2)})
			r.qids = append(r.qids, i)
		}
		return r
	}
	a, b := mk(), mk()
	finishRow(a, nil, 300, 7)
	finishRow(b, nil, 300, 7)
	if a.MRRCI != b.MRRCI || a.R10CI != b.R10CI {
		t.Fatal("bootstrap must be deterministic for a fixed seed")
	}
	if !(a.MRRCI.Lo <= a.MRR && a.MRR <= a.MRRCI.Hi) || a.MRRCI.Lo == a.MRRCI.Hi {
		t.Fatalf("CI %+v does not bracket mean %v", a.MRRCI, a.MRR)
	}
	// paired delta vs itself is exactly zero.
	c := mk()
	finishRow(c, a, 300, 7)
	if c.DMRRRef != 0 || c.DMRRCI.Lo != 0 || c.DMRRCI.Hi != 0 {
		t.Fatalf("self delta = %v %+v", c.DMRRRef, c.DMRRCI)
	}
}

func TestRunLookbackSynthetic(t *testing.T) {
	tasks := synthLookback()
	rep := RunLookback(tasks, LookbackOptions{Seed: 3, Boot: 50, NoLSA: true})
	if len(rep.Rows) == 0 {
		t.Fatalf("no rows; notes=%v", rep.Notes)
	}
	leak := map[string]float64{}
	for _, l := range rep.Leaks {
		leak[l.Check] = l.Value
	}
	if leak["residual slug mentions in stripped queries"] != 0 || leak["candidates not created before Q"] != 0 {
		t.Fatalf("leak checks failed: %v", rep.Leaks)
	}
	if leak["slug spans stripped (mode brief)"] == 0 {
		t.Fatal("expected stripped slug spans")
	}
	created := map[string]string{}
	for _, td := range tasks {
		created[td.Slug] = td.CreatedAt
	}
	for _, q := range rep.Queries {
		if len(q.Truth.Union()) == 0 {
			t.Fatalf("query %s has no truth", q.Slug)
		}
		for _, c := range q.cands {
			if created[c] >= q.Created || c == q.Slug {
				t.Fatalf("candidate %s of %s is not older", c, q.Slug)
			}
		}
		if strings.Contains(q.text, "topic") && strings.Contains(q.text, "-task-") {
			t.Fatalf("query %s still carries a slug", q.Slug)
		}
	}
	// On clean topic clusters a lexical ranker should find same-project
	// tasks well above chance.
	var found bool
	for _, r := range rep.Rows {
		if r.Scorer == "bm25-only" && r.Variant == VarFull && r.Mode == ModeBrief && r.Source == SrcProject {
			found = true
			if r.Hit5 < 0.9 {
				t.Fatalf("bm25 project hit@5 = %.2f on synthetic clusters", r.Hit5)
			}
		}
	}
	if !found {
		t.Fatal("missing bm25 project row")
	}
	var cut bool
	for _, r := range rep.Rows {
		cut = cut || r.Variant == VarCutoff
	}
	if !cut {
		t.Fatal("missing cutoff-variant rows")
	}
}

func TestRelatedRanksPastTopicFirst(t *testing.T) {
	tasks := synthLookback()
	q := tasks[len(tasks)-1]
	rel := Related(tasks, q.Slug, 3, 0, true) // minP 0: plain top 3
	if len(rel.Top) != 3 {
		t.Fatalf("top = %+v", rel.Top)
	}
	for _, h := range rel.Top {
		if !strings.HasPrefix(h.Slug, strings.SplitN(q.Slug, "-", 2)[0]) {
			t.Fatalf("related %s for %s is off-topic: %+v", h.Slug, q.Slug, rel.Top)
		}
		if h.Slug == q.Slug {
			t.Fatal("a task is not related to itself")
		}
	}
}

func TestRunLookbackDeterministic(t *testing.T) {
	tasks := synthLookback()
	// give some tasks prompts so the continuation-fitted scorers train
	for i := range tasks {
		if i%3 == 0 {
			tasks[i].SessionStarted = tasks[i].CreatedAt
			tasks[i].Prompts = []string{"check " + tasks[i].Name, "now fix the " + strings.Fields(tasks[i].Brief)[1] + " issue", "verify results again", "write the summary note"}
		}
	}
	a := RunLookback(tasks, LookbackOptions{Seed: 5, Boot: 40, NoLSA: true})
	b := RunLookback(tasks, LookbackOptions{Seed: 5, Boot: 40, NoLSA: true})
	if len(a.Rows) != len(b.Rows) {
		t.Fatal("row count differs")
	}
	for i := range a.Rows {
		x, y := a.Rows[i], b.Rows[i]
		if x.Scorer != y.Scorer || x.MRR != y.MRR || x.R10 != y.R10 || x.MRRCI != y.MRRCI || x.DMRRCI != y.DMRRCI {
			t.Fatalf("run-to-run difference in %s/%s/%s/%s:\n%+v\n%+v", x.Scorer, x.Variant, x.Mode, x.Source, x, y)
		}
	}
}
