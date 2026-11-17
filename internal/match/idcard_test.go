package match

import (
	"fmt"
	"testing"
)

func TestEntityTermsNormalizesVariants(t *testing.T) {
	variants := []string{
		"deploy to acme_eu_prod today",
		"deploy to acme-eu-prod today",
		"deploy to Acme.Eu.Prod today",
		"deploy to the acme eu prod cluster", // running words
	}
	for _, v := range variants {
		got := EntityTerms(v, true)
		if got["acme-eu-prod"] == nil {
			t.Errorf("%q: no acme-eu-prod term in %v", v, keysOf(got))
		}
		if got["prod"] == nil {
			t.Errorf("%q: no unigram prod", v)
		}
	}
	// plurals stem together
	if EntityTerms("two clusters", true)["cluster"] == nil {
		t.Error("clusters should stem to cluster")
	}
	// identifier compounds yield their contiguous sub-spans
	got := EntityTerms("see shop-prod-db-target", false)
	for _, w := range []string{"shop-prod-db-target", "shop-prod", "prod-db", "db-target", "shop-prod-db"} {
		if got[w] == nil || !got[w].shape {
			t.Errorf("want shape term %q in %v", w, keysOf(got))
		}
	}
	// stopwords, numbers and hex ids never become terms; dates do not form compounds
	got = EntityTerms("the 2026-05-28 run of deadbeefcafe and the", true)
	for _, bad := range []string{"the", "2026", "deadbeefcafe", "2026-05-28"} {
		if got[bad] != nil {
			t.Errorf("unexpected term %q", bad)
		}
	}
	// runs break at stopwords and punctuation
	got = EntityTerms("kafka broker, zookeeper quorum", true)
	if got["broker-zookeeper"] != nil {
		t.Error("run must break at a comma")
	}
	if got["kafka-broker"] == nil || got["zookeeper-quorum"] == nil {
		t.Errorf("missing run compounds: %v", keysOf(got))
	}
	// shape: mixed alnum, ALLCAPS and mid-sentence Capitalized words
	got = EntityTerms("We shifted db1 for Acme via DX", false)
	for _, w := range []string{"db1", "acme", "dx"} {
		if got[w] == nil || !got[w].shape {
			t.Errorf("%q should be shape-flagged", w)
		}
	}
	if got["shift"] == nil || got["shift"].shape {
		t.Error("plain word must not be shape-flagged")
	}
}

func keysOf(m map[string]*cardTerm) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// cardCorpus is a synthetic history: filler tasks that share everyday
// vocabulary, a target whose identity (orion-ledger service, Zephyr
// customer) lives only in its brief, and a long generic task that says
// "database migration" many times.
func cardCorpus() []TaskData {
	var c []TaskData
	for i := 0; i < 30; i++ {
		c = append(c, TaskData{
			Slug:      fmt.Sprintf("filler-task-%c", 'a'+i),
			Name:      fmt.Sprintf("Filler work item %d", i),
			CreatedAt: fmt.Sprintf("2026-01-%02dT00:00:00Z", i+1),
			Brief:     "Routine database migration and cleanup. Review the plan and update the docs.",
		})
	}
	c = append(c,
		TaskData{Slug: "ledger-cutover", Name: "Cut over the payments service", CreatedAt: "2026-02-01T00:00:00Z",
			Brief: "Move the orion-ledger service for Zephyr onto the new cluster zp-east-2. See also generic-db-hub."},
		TaskData{Slug: "generic-db-hub", Name: "Database migration hub", CreatedAt: "2026-02-02T00:00:00Z",
			Brief: "database migration database migration database migration plan review update docs " +
				"database migration cleanup database migration"},
	)
	return c
}

func TestBuildCards(t *testing.T) {
	corpus := cardCorpus()
	sm := NewSlugMatcher([]string{"ledger-cutover", "generic-db-hub"})
	strip := func(s string) string { x, _ := sm.Strip(s); return x }
	ci := BuildCards(corpus, 20, strip)
	card := ci.Cards["ledger-cutover"].W
	if len(card) > 20+len(EntityTerms("Cut over the payments service\nledger-cutover", true)) {
		t.Fatalf("card too large: %d", len(card))
	}
	for _, w := range []string{"ledger-cutover", "ledger", "payment"} { // name + slug always in
		if _, ok := card[w]; !ok {
			t.Errorf("name/slug term %q missing from card %v", w, card)
		}
	}
	for _, w := range []string{"orion-ledger", "zephyr"} { // distinctive body entities
		if _, ok := card[w]; !ok {
			t.Errorf("entity %q missing from card %v", w, card)
		}
	}
	for _, w := range []string{"database", "migration", "review"} { // common words (DF > 10%)
		if _, ok := card[w]; ok {
			t.Errorf("common word %q must not be on a card", w)
		}
	}
	// another task's slug in the body is stripped: no generic-db-hub identity leaks in
	for _, w := range []string{"generic-db-hub", "generic-db", "db-hub"} {
		if _, ok := card[w]; ok {
			t.Errorf("stripped slug term %q on the card", w)
		}
	}
	// k caps the body terms
	small := BuildCards(corpus, 3, strip).Cards["ledger-cutover"].W
	if _, ok := small["zephyr"]; ok && len(small) > 3 {
		t.Errorf("k=3 card holds body terms beyond the name: %v", small)
	}
}

func TestCardScorerRanksIdentityMatch(t *testing.T) {
	corpus := cardCorpus()
	m := NewModel(DefaultWeights)
	for _, td := range corpus {
		m.Upsert(td)
	}
	var cands []string
	for _, td := range corpus {
		cands = append(cands, td.Slug)
	}
	// the query names the target's entities, never its name words or slug
	text := "Zephyr asked about orion ledger latency after the database migration; plan a database migration review."
	ps := &PairSet{Model: m, Corpus: corpus}
	qr := newQueryRec(m, &termCache{m: m, t: map[string]map[string]bool{}}, "q", lbQuery(m, text), cands)
	qr.text = text
	ps.Queries = []QueryRec{qr}
	rankOf := func(s Scorer, slug string) int {
		bindTo(s, ps)
		for i, h := range rankAll(s, ps, 0, 1) {
			if h.Slug == slug {
				return i + 1
			}
		}
		return -1
	}
	for _, s := range []Scorer{
		&CardScorer{Label: "alone-q", K: 30, Norm: CardNormQuery, Alone: true},
		&CardScorer{Label: "alone-c", K: 30, Norm: CardNormCard, Alone: true},
		&CardScorer{Label: "fused", K: 30, Norm: CardNormQuery, Lambda: 1},
	} {
		if r := rankOf(s, "ledger-cutover"); r != 1 {
			t.Errorf("%s: ledger-cutover at rank %d, want 1", s.Name(), r)
		}
	}
	// card scores are normalized to [0,1]
	s := &CardScorer{K: 30, Norm: CardNormQuery, Alone: true}
	bindTo(s, ps)
	for _, c := range cands {
		if v := s.Score(qr.pair(0, c, false)); v < 0 || v > 1 {
			t.Errorf("%s: card score %v outside [0,1]", c, v)
		}
	}
	// a query with no entity overlap scores 0 everywhere
	ps.Queries[0].text = "nothing relevant here at all"
	bindTo(s, ps)
	for _, c := range cands {
		if v := s.Score(qr.pair(0, c, false)); v != 0 {
			t.Errorf("%s: score %v for a query with no shared entity", c, v)
		}
	}
}

func TestIDCardLineupNames(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range IDCardLineup(nil) {
		if seen[s.Name()] {
			t.Fatalf("duplicate scorer name %q", s.Name())
		}
		seen[s.Name()] = true
	}
	if !seen["bm25+0.25lsa+2card30q"] || !seen["card30c"] {
		t.Fatalf("lineup missing expected variants: %v", seen)
	}
}
