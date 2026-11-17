package match

import (
	"strings"
	"testing"
)

// promptTasks is a small corpus with clearly separated topics so a lexical
// nudge has an unambiguous right answer.
func promptTasks() []TaskData {
	return []TaskData{
		{Slug: "kafka-rebalance", Name: "fix kafka partition rebalance", CreatedAt: "2026-01-01T10:00:00Z", Status: "done",
			Brief: "Goal: the kafka broker keeps triggering partition rebalance storms under load."},
		{Slug: "postgres-replication", Name: "postgres logical replication slot", CreatedAt: "2026-01-02T10:00:00Z", Status: "done",
			Brief: "Goal: a postgres logical replication slot fills the disk on the primary."},
		{Slug: "invoice-billing", Name: "invoice billing export", CreatedAt: "2026-01-03T10:00:00Z", Status: "done",
			Brief: "Goal: customer invoice billing export drops a payment line."},
		{Slug: "terraform-outputs", Name: "terraform module outputs", CreatedAt: "2026-01-04T10:00:00Z", Status: "in-progress",
			Brief: "Goal: a terraform module provider output is not wired to the consumer."},
	}
}

// TestRelatedQueryContextWeightBites checks the context weight is a real,
// continuous knob (not a no-op): one candidate matches the PROMPT's rare
// term, another matches only the bound task's BRIEF. At weight 0 the
// prompt-match wins; raising the weight lifts the brief-match above it.
func TestRelatedQueryContextWeightBites(t *testing.T) {
	tasks := abstainCorpus() // filler so "quasar"/"zephyr" are rare terms
	tasks = append(tasks,
		TaskData{Slug: "bound-task", Name: "bound task", CreatedAt: "2026-04-01T10:00:00Z",
			Brief: "Goal: run the zephyr orchestration rollout end to end."},
		TaskData{Slug: "yprompt", Name: "quasar tuning job", CreatedAt: "2026-02-01T10:00:00Z",
			Brief: "Goal: tune the quasar scheduler throughput."},
		TaskData{Slug: "xbrief", Name: "zephyr integration", CreatedAt: "2026-02-02T10:00:00Z",
			Brief: "Goal: integrate the zephyr orchestration control plane."},
	)
	rankOf := func(res RelatedResult, slug string) int {
		for i, h := range res.Top {
			if h.Slug == slug {
				return i + 1
			}
		}
		return 0
	}
	// weight 0: only the prompt's rare term "quasar" matches -> yprompt shown,
	// xbrief (brief-only match) not surfaced by the bare prompt.
	r0 := RelatedQuery(tasks, RelatedQueryOptions{Prompt: "the quasar needs tuning",
		Self: "bound-task", ContextWeight: 0, K: 5, MinP: 0})
	if rankOf(r0, "yprompt") == 0 {
		t.Fatalf("w=0 should surface the prompt-matching task: %+v", r0.Top)
	}
	if rankOf(r0, "xbrief") != 0 {
		t.Fatalf("w=0 should NOT surface the brief-only task: %+v", r0.Top)
	}
	// large weight: the brief's "zephyr" term now dominates -> xbrief appears.
	r2 := RelatedQuery(tasks, RelatedQueryOptions{Prompt: "the quasar needs tuning",
		Self: "bound-task", ContextWeight: 4, K: 5, MinP: 0})
	if rankOf(r2, "xbrief") == 0 {
		t.Fatalf("a high context weight should surface the brief-matching task: %+v", r2.Top)
	}
}

// TestRelatedQueryFreeText ranks past tasks against a typed query with no
// bound task; the on-topic task must come first.
func TestRelatedQueryFreeText(t *testing.T) {
	tasks := promptTasks()
	res := RelatedQuery(tasks, RelatedQueryOptions{
		Prompt: "the logical replication slot is filling the postgres disk", K: 3, MinP: 0})
	if res.Mode != "query" {
		t.Fatalf("mode = %q, want query", res.Mode)
	}
	if res.Abstain {
		t.Fatal("unexpected abstain on a content-rich query")
	}
	if len(res.Top) == 0 || res.Top[0].Slug != "postgres-replication" {
		t.Fatalf("free-text top = %+v, want postgres-replication first", res.Top)
	}
	// No bound task to exclude, so every task is a candidate.
	if res.Candidates != len(tasks) {
		t.Fatalf("candidates = %d, want %d (all tasks)", res.Candidates, len(tasks))
	}
}

// abstainCorpus is a larger corpus (filler + a few topical tasks) so the
// document-frequency thresholds behave as they would in production: a
// topical term like "replication" or "invoice" is rare (<= 5%), while
// generic filler words are absent (DF 0).
func abstainCorpus() []TaskData {
	tasks := promptTasks() // kafka / postgres(logical replication) / invoice / terraform
	for i := 0; i < 26; i++ {
		tasks = append(tasks, TaskData{
			Slug:      "filler-" + string(rune('a'+i%26)) + "-" + string(rune('0'+i/26)),
			Name:      "filler widget line " + string(rune('a'+i)),
			CreatedAt: "2026-03-01T10:00:00Z", Status: "done",
			// "fix"/"it" recur across filler so they read as common words
			// (as they would in a real corpus), not distinctive terms.
			Brief: "Goal: fix and calibrate the " + string(rune('a'+i)) + " assembly jig and ship it.",
		})
	}
	return tasks
}

// TestRelatedQueryAbstains checks the informativeness-based abstain rule:
// generic prompts abstain; a rare/topical term or an identifier clears it.
func TestRelatedQueryAbstains(t *testing.T) {
	tasks := abstainCorpus()
	abstain := []string{"ok", "status?", "go ahead", "fix it", "go ahead please"}
	for _, p := range abstain {
		res := RelatedQuery(tasks, RelatedQueryOptions{Prompt: p, K: 3, MinP: 0})
		if !res.Abstain {
			t.Fatalf("generic prompt %q should abstain, got %+v", p, res.Top)
		}
		if len(res.Top) != 0 {
			t.Fatalf("abstaining prompt %q still showed hits: %+v", p, res.Top)
		}
	}
	// Informative prompts must NOT abstain (driver cases + identifier).
	keep := []string{"logical replication", "send invoices", "kafka partition rebalance", "check kafka-rebalance"}
	for _, p := range keep {
		res := RelatedQuery(tasks, RelatedQueryOptions{Prompt: p, K: 3, MinP: 0})
		if res.Abstain {
			t.Fatalf("informative prompt %q should NOT abstain", p)
		}
	}
}

// TestRelatedQuerySelfExclusion checks in-session mode drops the bound task
// and its children, and folds the bound task's brief in as context.
func TestRelatedQuerySelfExclusion(t *testing.T) {
	tasks := promptTasks()
	// give the bound task a child.
	tasks = append(tasks, TaskData{Slug: "terraform-outputs-fixup", Name: "terraform output fixup",
		Parent: "terraform-outputs", CreatedAt: "2026-01-05T10:00:00Z", Brief: "Goal: finish the terraform module output wiring."})
	res := RelatedQuery(tasks, RelatedQueryOptions{
		Prompt: "the provider output is still not wired to the consumer module",
		Self:   "terraform-outputs", ContextWeight: RelatedContextWeight,
		ExcludeChildren: true, K: 5, MinP: 0})
	if res.Mode != "prompt" {
		t.Fatalf("mode = %q, want prompt", res.Mode)
	}
	for _, h := range res.Top {
		if h.Slug == "terraform-outputs" {
			t.Fatal("bound task ranked against itself")
		}
		if h.Slug == "terraform-outputs-fixup" {
			t.Fatal("bound task's own child was not excluded")
		}
	}
	// self + child excluded from candidates.
	if res.Candidates != len(tasks)-2 {
		t.Fatalf("candidates = %d, want %d", res.Candidates, len(tasks)-2)
	}
}

// TestRelatedQueryExcludeBound checks free-text mode (Self=="") drops the
// session's bound task and its own children when ExcludeBound is set — a
// bound session never recalls the task it is sitting in — and includes
// everything when it is cleared (--include-self / unbound).
func TestRelatedQueryExcludeBound(t *testing.T) {
	tasks := promptTasks()
	// the bound task has a child that also matches the query.
	tasks = append(tasks, TaskData{Slug: "postgres-replication-child", Name: "replication slot follow-up",
		Parent: "postgres-replication", CreatedAt: "2026-01-06T10:00:00Z",
		Brief: "Goal: the postgres logical replication slot still fills the disk."})
	prompt := "the logical replication slot is filling the postgres disk"

	// Unbound / --include-self: the bound task is a legitimate candidate and,
	// being the on-topic task, ranks first.
	open := RelatedQuery(tasks, RelatedQueryOptions{Prompt: prompt, K: 5, MinP: 0})
	if open.Candidates != len(tasks) {
		t.Fatalf("include-self candidates = %d, want %d", open.Candidates, len(tasks))
	}
	if len(open.Top) == 0 || open.Top[0].Slug != "postgres-replication" {
		t.Fatalf("include-self top = %+v, want postgres-replication first", open.Top)
	}

	// Bound to postgres-replication: that task AND its child must be absent.
	ex := RelatedQuery(tasks, RelatedQueryOptions{Prompt: prompt, K: 5, MinP: 0,
		ExcludeBound: "postgres-replication"})
	if ex.Mode != "query" {
		t.Fatalf("mode = %q, want query (free text, no Self)", ex.Mode)
	}
	if ex.Candidates != len(tasks)-2 {
		t.Fatalf("excludeBound candidates = %d, want %d (self + child dropped)", ex.Candidates, len(tasks)-2)
	}
	for _, h := range ex.Top {
		if h.Slug == "postgres-replication" {
			t.Fatal("free-text recall surfaced the bound task itself")
		}
		if h.Slug == "postgres-replication-child" {
			t.Fatal("free-text recall surfaced the bound task's own child")
		}
	}
}

// TestRelatedWithExcludeBound checks slug mode also drops a DIFFERENT bound
// task (not the query slug) when ExcludeBound is set.
func TestRelatedWithExcludeBound(t *testing.T) {
	tasks := promptTasks()
	// Query is terraform-outputs; the session is bound to postgres-replication.
	// Rank over everything (--all) so date filtering isn't the reason it drops.
	ex := RelatedWith(tasks, "terraform-outputs", RelatedOptions{K: 10, MinP: 0,
		ExcludeBound: "postgres-replication"})
	for _, h := range ex.Top {
		if h.Slug == "postgres-replication" {
			t.Fatal("slug-mode recall surfaced the bound task while bound elsewhere")
		}
	}
	// Candidates exclude both the query slug and the bound slug.
	if ex.Candidates != len(tasks)-2 {
		t.Fatalf("candidates = %d, want %d (query + bound dropped)", ex.Candidates, len(tasks)-2)
	}
}

// TestRelatedQueryNoPadding checks the zero-overlap floor: a query with a
// single distinctive term never pads unrelated tasks to fill k, even at
// MinP 0 with a large k.
func TestRelatedQueryNoPadding(t *testing.T) {
	tasks := promptTasks()
	res := RelatedQuery(tasks, RelatedQueryOptions{
		Prompt: "kafka partition rebalance storm on the broker", K: 10, MinP: 0})
	if len(res.Top) == 0 {
		t.Fatal("expected at least the on-topic kafka task")
	}
	for _, h := range res.Top {
		// every shown task must share a real term with the query; the
		// invoice/postgres tasks have no kafka/broker/rebalance overlap.
		if h.Slug == "invoice-billing" {
			t.Fatalf("padded an unrelated task with no overlap: %+v", res.Top)
		}
	}
	if res.Top[0].Slug != "kafka-rebalance" {
		t.Fatalf("top = %+v, want kafka-rebalance", res.Top)
	}
}

// TestRelatedWithOverlapFloor checks the slug mode also drops no-overlap
// tasks instead of padding to k.
func TestRelatedWithOverlapFloor(t *testing.T) {
	// an isolated task whose only shared words with the rest are stopwords.
	tasks := append(promptTasks(), TaskData{Slug: "zzz-orphan", Name: "orphan island",
		CreatedAt: "2026-02-01T10:00:00Z", Brief: "Goal: nothing here resembles the others at all."})
	res := RelatedWith(tasks, "zzz-orphan", RelatedOptions{K: 10, MinP: 0, PastOnly: true})
	for _, h := range res.Top {
		if strings.HasPrefix(h.Slug, "kafka") || strings.HasPrefix(h.Slug, "postgres") ||
			strings.HasPrefix(h.Slug, "invoice") || strings.HasPrefix(h.Slug, "terraform") {
			// allowed only if it genuinely shares a term; "orphan island /
			// nothing resembles the others" shares none of the topic words.
			t.Fatalf("padded unrelated task %s for an orphan query: %+v", h.Slug, res.Top)
		}
	}
}
