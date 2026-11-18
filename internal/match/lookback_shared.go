package match

import (
	"hash/fnv"
	"math"
	"sort"
	"strings"
)

// This file holds the query-building and ranking primitives that BOTH the
// shipped recall path (related.go, prompt.go, calibrate.go) and the held-out
// eval (lookback.go, behind the `recalleval` build tag) depend on. Keeping
// them here, untagged, lets `flow recall` and `flow recall train --calibrate`
// ship in the default binary while the benchmark/report machinery is compiled
// in only with `-tags recalleval`. See doc.go and docs/recall.md.

// Query modes.
const (
	ModeBrief   = "brief"
	ModePrompts = "brief+prompts"
)

// LookbackQueryTokens caps a lookback query's tokens (briefs are long;
// prompts keep MaxPromptTokens).
const LookbackQueryTokens = 1500

// Miss-bucket / mismatch document-frequency cuts (share of indexed tasks).
// Used by prompt.go's shipped mismatch diagnostics and the eval misses report.
const (
	CommonDF   = 0.30 // template / ubiquitous words ("goal", "done", "scope")
	DistinctDF = 0.05 // a word this rare is distinctive
)

// LookbackOptions configures the query build (shared) and RunLookback (eval).
type LookbackOptions struct {
	Seed       uint64
	Weights    Weights
	PromptsN   int      // mode brief+prompts: first N informative prompts (default 3)
	Folds      int      // test folds (default EvalFolds); one extra, oldest fold only trains
	Boot       int      // bootstrap resamples (default 1000)
	Examples   int      // example queries to list (default 5)
	StripSlugs []string // extra slugs to strip from queries (e.g. excluded tasks)
	GenericTag []string // generic tags (default DefaultGenericTags)
	NoLSA      bool     // skip the LSA fusion scorer
	NoCutoff   bool     // skip the notes<=Q variant
	Structure  bool     // add the structure-prior experiment (structure.go); needs LSA
	NoConf     bool     // skip the confidence cut-off analysis
}

// LBHit is one ranked candidate.
type LBHit struct {
	Slug     string   `json:"slug"`
	Score    float64  `json:"score"`
	Cov      float64  `json:"cov,omitempty"`      // query coverage of this candidate
	Relevant []string `json:"relevant,omitempty"` // truth sources, if relevant
}

// queryText renders Q's query side for mode (unstripped): its name + brief,
// optionally followed by its first n informative prompts.
func queryText(td TaskData, mode string, n int) (string, bool) {
	t := td.Name + "\n" + td.Brief
	if mode == ModeBrief {
		return t, true
	}
	inf := informativePrompts(td)
	if len(inf) == 0 {
		return "", false
	}
	if len(inf) > n {
		inf = inf[:n]
	}
	return t + "\n" + strings.Join(inf, "\n"), true
}

// termCache memoizes candidates' unigram term sets for Jaccard.
type termCache struct {
	m *Model
	t map[string]map[string]bool
}

func (tc *termCache) of(slug string) map[string]bool {
	if x, ok := tc.t[slug]; ok {
		return x
	}
	var x map[string]bool
	if e := tc.m.Tasks[slug]; e != nil {
		x = unigramSet(e.termStrings(tc.m))
	}
	tc.t[slug] = x
	return x
}

// newQueryRec measures q against cands on m (as BuildPairs does).
func newQueryRec(m *Model, tc *termCache, owner string, q Query, cands []string) QueryRec {
	qr := QueryRec{Owner: owner, Q: q, Cands: cands, sig: map[string]Signals{},
		jac: map[string]float64{}, ovl: map[string]float64{}}
	qterms := map[string]bool{}
	for t := range q.tf {
		qterms[t] = true
	}
	qterms = unigramSet(qterms)
	for _, c := range cands {
		sg := m.SignalsFor(q, c)
		qr.sig[c] = sg
		qr.jac[c], qr.ovl[c] = setSims(qterms, tc.of(c))
		qr.top[0].add(c, sg.Sim)
		qr.top[1].add(c, sg.KNN)
		qr.top[2].add(c, sg.Cov)
		qr.top[3].add(c, sg.BM25)
		qr.primary = append(qr.primary, sg.Primary())
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(qr.primary)))
	return qr
}

func lbQuery(m *Model, text string) Query {
	return m.queryFromCounts(Counts(text, LookbackQueryTokens))
}

func slugHash(seed uint64, s string) uint64 {
	h := fnv.New64a()
	var b [8]byte
	for i := range b {
		b[i] = byte(seed >> (8 * i))
	}
	h.Write(b[:])
	h.Write([]byte(s))
	return h.Sum64()
}

// rankAll scores every candidate of query qi with s (bound/prepared on
// ps) and returns them best first (ties by seeded slug hash).
func rankAll(s Scorer, ps *PairSet, qi int, seed uint64) []LBHit {
	qr := &ps.Queries[qi]
	out := make([]LBHit, 0, len(qr.Cands))
	for _, c := range qr.Cands {
		// Quantize: scores sum over map-ordered terms, so float order noise
		// (~1e-16) would otherwise flip exact ties between runs.
		out = append(out, LBHit{Slug: c, Score: math.Round(s.Score(qr.pair(qi, c, false))*1e9) / 1e9, Cov: qr.sig[c].Cov})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return slugHash(seed, out[i].Slug) < slugHash(seed, out[j].Slug)
	})
	return out
}

// bindTo lets a scorer that builds its own per-PairSet index (the LSA and
// identity-card scorers) prepare against ps before it is scored.
func bindTo(s Scorer, ps *PairSet) {
	if p, ok := s.(Preparer); ok {
		p.Prepare(ps)
	}
}
