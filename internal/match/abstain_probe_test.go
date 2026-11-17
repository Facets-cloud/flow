package match

import (
	"fmt"
	"testing"
)

// Regression probe for the DF-cliff abstain bug (user-reported on the
// installed build): "redis data migration" abstained as too generic while
// "redis data copy" ranked, because the old rule bucketed terms by hard DF
// cliffs (one term < 5% OR two < 30%). Here redis sits just over 5% and
// migration just over 30%, so the query counted as a single mid term and
// abstained. The smooth summed-IDF test has no cliff, so both queries clear
// the bar and surface the same redis task.
func abstainProbeCorpus() []TaskData {
	day := func(n int) string { return fmt.Sprintf("2026-02-%02dT10:00:00Z", n) }
	var tasks []TaskData
	add := func(slug, brief string, i int) {
		tasks = append(tasks, TaskData{Slug: slug, Name: slug, CreatedAt: day(i), Brief: brief})
	}
	// The one redis task (redis DF = 1/19 ≈ 5.3%, just over the old 5% cliff).
	add("redis-keyspace-copy", "copy the redis keyspace and migration of the data from source to target", 1)
	// "migration" in 5 more tasks -> migration DF = 6/19 ≈ 31.6% (just over 30%).
	for i, s := range []string{"schema-migration", "billing-migration", "account-migration", "ledger-migration", "inventory-migration"} {
		add(s, "database migration of the data for this service rollout", 2+i)
	}
	// "data" spread wide so it is a common (>30%) term, not distinctive.
	for i := 0; i < 7; i++ {
		add(fmt.Sprintf("data-task-%d", i), "the data pipeline export report for the quarter", 10+i)
	}
	// unrelated filler.
	for i, b := range []string{
		"kafka broker partition rebalance consumer offset",
		"terraform module provider output variable",
		"oncall rotation pager escalation incident",
		"invoice refund reconciliation ledger entry",
		"frontend dashboard widget layout styling",
		"auth token rotation session expiry",
	} {
		add(fmt.Sprintf("filler-%d", i), b, 20+i)
	}
	return tasks
}

func TestRecallAbstainSmoothInformativeness(t *testing.T) {
	tasks := abstainProbeCorpus()
	m := NewModel(DefaultWeights)
	for _, td := range tasks {
		m.Upsert(td)
	}

	// Both the reported query and its ranking sibling must be informative
	// enough to rank (they share the "redis" + "migration"/"copy" topical mass).
	for _, q := range []string{"redis data migration", "redis data copy"} {
		if m.promptAbstains(q) {
			t.Errorf("%q should not abstain: it carries real topical mass", q)
		}
		res := RelatedQuery(tasks, RelatedQueryOptions{Prompt: q, K: 3, MinP: 0})
		if res.Abstain {
			t.Errorf("%q: RelatedQuery abstained", q)
			continue
		}
		found := false
		for _, h := range res.Top {
			if h.Slug == "redis-keyspace-copy" {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: expected redis-keyspace-copy in the results, got %v", q, slugsOf(res.Top))
		}
	}

	// Genuinely content-free prompts (stopwords and words the user has never
	// worked on) still abstain — the smooth bar keeps the zero-mass floor.
	for _, q := range []string{"fix it please", "go ahead now", "can you do that"} {
		if !m.promptAbstains(q) {
			t.Errorf("%q should abstain (no distinctive mass)", q)
		}
	}
}

func slugsOf(hits []RelatedHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Slug
	}
	return out
}
