package match

import (
	"fmt"
	"testing"
)

// Regression probe for the single-strong-word bug: a
// free-text query whose best candidate shares only one or two of its
// informative terms must not be labelled strong just for ranking #1. Query
// coverage — the IDF-weighted share of the query a candidate matches — is the
// layer-3 feature that guards this. The fixtures are synthetic so the probe is
// hermetic and does not depend on any user's history.

// covProbeCorpus builds a corpus in which NO task covers the 8-term query
// well: the best candidate matches two terms (hammered, so it ranks #1 on raw
// score with score/self ≈ 1), the rest match one term each. Before the fix the
// #1 hit was labelled strong on rank alone; after it, its ~0.25 coverage keeps
// it out of strong. This mirrors the real repro ("aws partner network portal"
// → a task matching only "partner" ranked strong) without the private data.
func covProbeCorpus() (tasks []TaskData, query string) {
	query = "alpha bravo charlie delta echo foxtrot golf hotel"
	day := func(n int) string { return fmt.Sprintf("2026-01-%02dT10:00:00Z", n) }

	// best-partial: only "alpha" and "bravo", hammered -> highest BM25.
	bestPartial := "alpha bravo alpha bravo alpha bravo alpha bravo " +
		"widget gadget sprocket flange grommet trunnion the plan ships weekly"
	tasks = []TaskData{
		{Slug: "best-partial", Name: "alpha bravo board", CreatedAt: day(1), Brief: bestPartial},
		// single-term candidates (each covers ~1/8).
		{Slug: "one-charlie", Name: "charlie notes", CreatedAt: day(2), Brief: "charlie charlie review of the charlie rollout afterwards"},
		{Slug: "one-delta", Name: "delta notes", CreatedAt: day(3), Brief: "delta delta the delta follow-up and its summary"},
	}
	// filler with unrelated vocabulary so the query terms keep a low DF.
	fillers := []string{
		"invoice billing payment refund ledger reconciliation",
		"kafka broker partition rebalance consumer offset",
		"postgres replication slot wal sender receiver",
		"terraform module provider output variable state",
		"oncall rotation pager escalation incident runbook",
	}
	for i, b := range fillers {
		tasks = append(tasks, TaskData{Slug: fmt.Sprintf("filler-%d", i), Name: fmt.Sprintf("filler %d", i),
			CreatedAt: day(4 + i), Brief: b})
	}
	return tasks, query
}

func TestRecallCoverageGuardsStrongLabel(t *testing.T) {
	tasks, query := covProbeCorpus()

	m := NewModel(DefaultWeights)
	for _, td := range tasks {
		m.Upsert(td)
	}
	q := lbQuery(m, query)

	res := RelatedQuery(tasks, RelatedQueryOptions{Prompt: query, K: 5, MinP: 0})
	if len(res.Top) == 0 {
		t.Fatal("expected some matches for the probe query")
	}

	cov := map[string]float64{}
	for _, h := range res.Top {
		cov[h.Slug] = m.Coverage(q, h.Slug)
	}

	// Probe sanity: the best candidate ranks #1 but still covers < half.
	if res.Top[0].Slug != "best-partial" {
		t.Fatalf("probe mis-built: expected best-partial to rank #1, got %q", res.Top[0].Slug)
	}
	if c := cov["best-partial"]; c >= 0.45 {
		t.Fatalf("probe mis-built: best-partial should cover a minority of the query, got %.3f", c)
	}

	// The core guard: no STRONG match may cover less than ~half the query's
	// informative weight — exactly the "strong on one shared word" bug.
	for _, h := range res.Top {
		if h.Label == LabelStrong && cov[h.Slug] < 0.45 {
			t.Errorf("strong match %q covers only %.3f of the query (< 0.45): a low-coverage "+
				"match must not earn strong on rank alone (p=%.3f)", h.Slug, cov[h.Slug], h.P)
		}
	}
}

// TestRecallCoverageLiftsConfidence pins the mechanism directly: holding the
// other confidence features fixed, a higher-coverage item is scored at least as
// confident as a lower-coverage one, and the shipped weight makes the gap real.
func TestRecallCoverageLiftsConfidence(t *testing.T) {
	// Two single-item lists, identical but for the top item's coverage.
	mk := func(coverage float64) *ConfList {
		return &ConfList{Query: "q", Self: 1, Tokens: 6,
			Items: []ConfItem{{Slug: "a", Score: 0.9, Cov: coverage}}}
	}
	low := RelatedConf.Prob(mk(0.20), 0)
	high := RelatedConf.Prob(mk(0.90), 0)
	if high <= low {
		t.Fatalf("higher coverage must raise p: cov .90 -> %.3f should exceed cov .20 -> %.3f", high, low)
	}
	if high-low < 0.05 {
		t.Errorf("coverage lift too weak to matter: %.3f vs %.3f (Δ %.3f)", high, low, high-low)
	}
}
