package match

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
)

// ---------- lookback eval ----------
//
// Query = task Q as it looked near its start: its name + brief (mode
// "brief"), or that plus its first PromptsN informative prompts (mode
// "brief+prompts"). Every task slug and its close variants are stripped
// from the query text, otherwise mention truth would be found through
// the candidates' name fields. Candidates = every task created strictly
// before Q, any status. Candidates keep their full brief, notes and
// prompts (in real use past tasks are complete); the "cutoff" variant
// drops candidate notes dated after Q's creation day to measure that
// leak. Structure-derived truth (parent, project, family, playbook,
// tag) is never a feature of any scorer here.

// Query modes.
const (
	ModeBrief   = "brief"
	ModePrompts = "brief+prompts"
)

// ModeBriefSubset is a report-only slice: mode brief restricted to the
// tasks that also have a brief+prompts query, so the two modes compare on
// the same tasks.
const ModeBriefSubset = "brief@prompt-tasks"

// LookbackModes lists the query modes in report order.
var LookbackModes = []string{ModeBrief, ModePrompts}

// Note variants.
const (
	VarFull   = "full-notes"
	VarCutoff = "notes<=Q"
)

// LookbackQueryTokens caps a lookback query's tokens (briefs are long;
// prompts keep MaxPromptTokens).
const LookbackQueryTokens = 1500

// LookbackRef is the reference scorer for paired deltas.
const LookbackRef = "bm25-only"

// LookbackOptions configures RunLookback.
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

// LBQuery is one lookback query (never carries its text out).
type LBQuery struct {
	Slug     string   `json:"slug"`
	Created  string   `json:"created"`
	Mode     string   `json:"mode"`
	Fold     int      `json:"fold"`
	Stripped int      `json:"stripped_mentions"`
	Tokens   int      `json:"tokens"`
	Cands    int      `json:"candidates"`
	Truth    TruthSet `json:"truth"`

	text  string
	cands []string
}

// QueryMetrics are one query's ranking metrics against one truth set.
type QueryMetrics struct {
	R5, R10, R20, MRR, NDCG10, Hit5 float64
}

func metricsOf(rank map[string]int, truth []string) QueryMetrics {
	var m QueryMetrics
	if len(truth) == 0 {
		return m
	}
	first := math.MaxInt
	var h5, h10, h20 int
	var dcg float64
	for _, t := range truth {
		r, ok := rank[t]
		if !ok {
			continue
		}
		if r < first {
			first = r
		}
		if r <= 5 {
			h5++
		}
		if r <= 10 {
			h10++
			dcg += 1 / math.Log2(float64(r)+1)
		}
		if r <= 20 {
			h20++
		}
	}
	n := float64(len(truth))
	m.R5, m.R10, m.R20 = float64(h5)/n, float64(h10)/n, float64(h20)/n
	if first != math.MaxInt {
		m.MRR = 1 / float64(first)
	}
	var idcg float64
	for i := 1; i <= min(10, len(truth)); i++ {
		idcg += 1 / math.Log2(float64(i)+1)
	}
	m.NDCG10 = dcg / idcg
	if h5 > 0 {
		m.Hit5 = 1
	}
	return m
}

// CI is a bootstrap 95% interval.
type CI struct {
	Lo float64 `json:"lo"`
	Hi float64 `json:"hi"`
}

// LBRow is one scorer's aggregate on one (variant, mode, truth source).
type LBRow struct {
	Scorer  string  `json:"scorer"`
	Variant string  `json:"variant"`
	Mode    string  `json:"mode"`
	Source  string  `json:"source"` // "all" or a TruthSources entry
	Queries int     `json:"queries"`
	R5      float64 `json:"recall@5"`
	R10     float64 `json:"recall@10"`
	R20     float64 `json:"recall@20"`
	MRR     float64 `json:"mrr"`
	NDCG10  float64 `json:"ndcg@10"`
	Hit5    float64 `json:"hit@5"`
	MRRCI   CI      `json:"mrr_ci"`
	R10CI   CI      `json:"recall@10_ci"`
	DMRRRef float64 `json:"d_mrr_vs_ref"` // paired, vs LookbackRef
	DMRRCI  CI      `json:"d_mrr_vs_ref_ci"`
	DR10Ref float64 `json:"d_recall@10_vs_ref"`
	DR10CI  CI      `json:"d_recall@10_vs_ref_ci"`
	// Paired vs RelatedScorer (bm25+0.25lsa), when it is in the lineup.
	DMRRRel   float64 `json:"d_mrr_vs_related,omitempty"`
	DMRRRelCI CI      `json:"d_mrr_vs_related_ci"`
	DR10Rel   float64 `json:"d_recall@10_vs_related,omitempty"`
	DR10RelCI CI      `json:"d_recall@10_vs_related_ci"`
	perQuery  []QueryMetrics
	qids      []int
}

// SourceCount is how many queries (with enough text) and truth pairs a
// source yields.
type SourceCount struct {
	Source       string  `json:"source"`
	Queries      int     `json:"queries"`       // query tasks with >=1 truth item (any text)
	QueriesBrief int     `json:"queries_brief"` // ...that survive as mode-brief queries
	QueriesPr    int     `json:"queries_brief+prompts"`
	Pairs        int     `json:"pairs"`
	MeanPerQ     float64 `json:"mean_per_query"`
	OnlySource   int     `json:"queries_only_this_source"`
}

// LBExample is one query's top-5 under the best scorer, with truth.
type LBExample struct {
	Query  string              `json:"query"`
	Mode   string              `json:"mode"`
	Scorer string              `json:"scorer"`
	Top5   []LBHit             `json:"top5"`
	Truth  map[string][]string `json:"truth"` // slug -> sources
	Ranks  map[string]int      `json:"truth_ranks"`
}

// LBHit is one ranked candidate.
type LBHit struct {
	Slug     string   `json:"slug"`
	Score    float64  `json:"score"`
	Cov      float64  `json:"cov,omitempty"`      // query coverage of this candidate
	Relevant []string `json:"relevant,omitempty"` // truth sources, if relevant
}

// MissCount buckets truth items the best scorer ranked below 20.
type MissCount struct {
	Source     string `json:"source"`
	Items      int    `json:"truth_items"`
	Top20      int    `json:"in_top20"`
	Missed     int    `json:"missed"`
	NoOverlap  int    `json:"no_text_overlap"`     // shares no word except brief-template/common ones (DF > CommonDF)
	Vocabulary int    `json:"vocabulary_mismatch"` // shares only everyday words, none distinctive (DF <= DistinctDF)
	Identifier int    `json:"identifier_mismatch"` // shares distinctive words, but none of the candidate's distinctive name/slug words
	Outranked  int    `json:"outranked"`           // shares distinctive name words too, still crowded out
	EmptyCand  int    `json:"empty_candidate"`     // candidate has almost no text
}

// Miss-bucket document-frequency cuts (share of indexed tasks).
const (
	CommonDF   = 0.30 // template / ubiquitous words ("goal", "done", "scope")
	DistinctDF = 0.05 // a word this rare is distinctive
)

// LeakCheck is one leakage measurement.
type LeakCheck struct {
	Check  string  `json:"check"`
	Value  float64 `json:"value"`
	Detail string  `json:"detail"`
}

// LookbackReport is the full lookback eval output.
type LookbackReport struct {
	Seed          uint64         `json:"seed"`
	Tasks         int            `json:"tasks"`
	GenericTags   []string       `json:"generic_tags"`
	TruthCounts   []SourceCount  `json:"truth_counts"`
	MentionRules  []MentionStat  `json:"mention_rules"`
	SkippedNoText int            `json:"skipped_no_text"`
	Queries       []LBQuery      `json:"queries"`
	FoldStarts    []string       `json:"fold_starts"`
	TestQueries   map[string]int `json:"test_queries"`
	Rows          []LBRow        `json:"rows"`
	Best          string         `json:"best_scorer"`
	Examples      []LBExample    `json:"examples"`
	Misses        []MissCount    `json:"misses"`
	Leaks         []LeakCheck    `json:"leakage_checks"`
	Weights       []LBWeights    `json:"lookback_weights"`
	Confidence    *ConfAnalysis  `json:"confidence_cutoff,omitempty"`
	Notes         []string       `json:"notes"`
}

// LBWeights is a lookback-trained scorer's learned weights for a fold.
type LBWeights struct {
	Scorer  string             `json:"scorer"`
	Fold    int                `json:"fold"`
	Train   int                `json:"train_pairs"`
	Weights map[string]float64 `json:"weights"`
}

// lbScorer is one entry in the lookback lineup: a fixed scorer instance used
// for every query.
type lbScorer struct {
	name string
	// noCut: skip in the notes<=Q variant (scorers built on the LSA space).
	noCut bool
	fixed Scorer
}

// lookbackLineup is the kept scorer lineup for the recall eval: the lexical
// baselines plus the shipped layer-2 base ranker (bm25+0.25lsa). bm25-only is
// LookbackRef, the reference for paired deltas.
func lookbackLineup(noLSA bool) []lbScorer {
	ls := []lbScorer{
		{name: "baseline:jaccard", fixed: &ThresholdScorer{Label: "baseline:jaccard", F: func(p Pair) float64 { return p.Jaccard }}},
		{name: "bm25-only", fixed: &ThresholdScorer{Label: "bm25-only", F: func(p Pair) float64 { return p.A.BM25 }}},
		{name: "cosine-only", fixed: &ThresholdScorer{Label: "cosine-only", F: func(p Pair) float64 { return p.A.Sim }}},
	}
	if !noLSA {
		ls = append(ls, lbScorer{name: RelatedScorer, fixed: relatedScorer(), noCut: true})
	}
	return ls
}

// queryText renders Q's query side for mode (unstripped).
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

// lbResult is one query's ranking under one scorer.
type lbResult struct {
	ranks map[string]int // truth-union item -> 1-based rank
	top   []LBHit        // top-5
}

func resultOf(hits []LBHit, truth []string) lbResult {
	want := map[string]bool{}
	for _, t := range truth {
		want[t] = true
	}
	r := lbResult{ranks: map[string]int{}}
	for i, h := range hits {
		if want[h.Slug] {
			r.ranks[h.Slug] = i + 1
		}
	}
	r.top = append([]LBHit(nil), hits[:min(5, len(hits))]...)
	return r
}

// RunLookback runs the lookback eval over tasks (matcher tasks already
// excluded by the caller).
func RunLookback(tasks []TaskData, opt LookbackOptions) LookbackReport {
	if opt.Seed == 0 {
		opt.Seed = DefaultSeed
	}
	if opt.Weights == (Weights{}) {
		opt.Weights = DefaultWeights
	}
	if opt.PromptsN <= 0 {
		opt.PromptsN = 3
	}
	if opt.Folds <= 0 {
		opt.Folds = EvalFolds
	}
	if opt.Boot <= 0 {
		opt.Boot = 1000
	}
	if opt.Examples < 0 {
		opt.Examples = 0
	} else if opt.Examples == 0 {
		opt.Examples = 5
	}
	if opt.GenericTag == nil {
		opt.GenericTag = DefaultGenericTags
	}
	tasks = append([]TaskData(nil), tasks...)
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].CreatedAt != tasks[j].CreatedAt {
			return tasks[i].CreatedAt < tasks[j].CreatedAt
		}
		return tasks[i].Slug < tasks[j].Slug
	})
	rep := LookbackReport{Seed: opt.Seed, Tasks: len(tasks), TestQueries: map[string]int{}}
	var all []string
	for _, t := range tasks {
		all = append(all, t.Slug)
	}
	all = append(all, opt.StripSlugs...)
	sm := NewSlugMatcher(all)
	generic := GenericTags(tasks, opt.GenericTag)
	rep.GenericTags = sortedKeys(generic)
	truth := DeriveTruth(tasks, sm, generic)
	rep.MentionRules = MentionStats(tasks, sm)
	byslug := map[string]TaskData{}
	for _, t := range tasks {
		byslug[t.Slug] = t
	}

	// ---- queries ----
	var qs []LBQuery
	for _, q := range tasks {
		ts, ok := truth[q.Slug]
		if !ok {
			continue
		}
		var cands []string
		for _, c := range tasks {
			if c.CreatedAt < q.CreatedAt && c.Slug != q.Slug {
				cands = append(cands, c.Slug)
			}
		}
		for _, mode := range LookbackModes {
			raw, ok := queryText(q, mode, opt.PromptsN)
			if !ok {
				continue
			}
			text, n := sm.Strip(raw)
			tok := len(Tokenize(text))
			if tok < MinQueryTokens {
				if mode == ModeBrief {
					rep.SkippedNoText++
				}
				continue
			}
			qs = append(qs, LBQuery{Slug: q.Slug, Created: q.CreatedAt, Mode: mode, Stripped: n, Tokens: tok,
				Cands: len(cands), Truth: ts, text: text, cands: cands})
		}
	}
	rep.TruthCounts = truthCounts(truth, qs)

	// ---- folds: split mode-brief queries by time into Folds+1 chunks ----
	var times []string
	for _, q := range qs {
		if q.Mode == ModeBrief {
			times = append(times, q.Created)
		}
	}
	if len(times) < opt.Folds+1 {
		rep.Notes = append(rep.Notes, "too few queries for the fold split")
		rep.Queries = qs
		return rep
	}
	var starts []string
	for i := 1; i <= opt.Folds; i++ {
		starts = append(starts, times[i*len(times)/(opt.Folds+1)])
	}
	rep.FoldStarts = starts
	for i := range qs {
		f := 0
		for _, s := range starts {
			if qs[i].Created >= s {
				f++
			}
		}
		qs[i].Fold = f
	}
	rep.Queries = qs

	// ---- full-notes index and one PairSet per mode ----
	m := NewModel(opt.Weights)
	for _, t := range tasks {
		m.Upsert(t)
	}
	tc := &termCache{m: m, t: map[string]map[string]bool{}}
	pss := map[string]*PairSet{}
	qidx := map[string][]int{} // mode -> indices into qs (parallel to ps.Queries)
	for _, mode := range LookbackModes {
		ps := &PairSet{Model: m, Corpus: tasks}
		for i, q := range qs {
			if q.Mode != mode {
				continue
			}
			ps.Queries = append(ps.Queries, newQueryRec(m, tc, q.Slug, lbQuery(m, q.text), q.cands))
			qidx[mode] = append(qidx[mode], i)
		}
		pss[mode] = ps
	}
	// local views of qs per mode, aligned with ps.Queries
	modeQs := map[string][]LBQuery{}
	for _, mode := range LookbackModes {
		for _, i := range qidx[mode] {
			modeQs[mode] = append(modeQs[mode], qs[i])
		}
	}

	lineup := lookbackLineup(opt.NoLSA)
	bps := pss[ModeBrief]

	// ---- score: results[variant][mode][scorer][qi] ----
	type key struct{ variant, mode, scorer string }
	results := map[key][]*lbResult{}
	// score ranks queries qis of mq (aligned with ps.Queries) under every
	// scorer in ls; out[name][qi].
	score := func(ls []lbScorer, ps *PairSet, mq []LBQuery, qis []int) map[string][]*lbResult {
		out := map[string][]*lbResult{}
		for _, l := range ls {
			res := make([]*lbResult, len(mq))
			out[l.name] = res
			bindTo(l.fixed, ps)
			for _, qi := range qis {
				r := resultOf(rankAll(l.fixed, ps, qi, opt.Seed), mq[qi].Truth.Union())
				res[qi] = &r
			}
		}
		return out
	}
	testIdx := func(mq []LBQuery) []int {
		var out []int
		for qi, q := range mq {
			if q.Fold >= 1 {
				out = append(out, qi)
			}
		}
		return out
	}
	for _, mode := range LookbackModes {
		ti := testIdx(modeQs[mode])
		rep.TestQueries[mode] = len(ti)
		for name, res := range score(lineup, pss[mode], modeQs[mode], ti) {
			results[key{VarFull, mode, name}] = res
		}
	}

	// ---- confidence cut-off: which of the top 10 to actually show ----
	if !opt.NoConf {
		var cs Scorer
		name := ""
		for _, l := range lineup {
			if l.fixed != nil && (l.name == RelatedScorer || (cs == nil && l.name == LookbackRef)) {
				cs, name = l.fixed, l.name
			}
		}
		if cs != nil {
			bindTo(cs, bps)
			var lists []*ConfList
			for qi, q := range modeQs[ModeBrief] {
				hits := rankAll(cs, bps, qi, opt.Seed)
				qr := &bps.Queries[qi]
				l := &ConfList{Query: q.Slug, Fold: q.Fold, Tokens: q.Tokens, Self: selfScore(cs, bps, qi),
					strict: setOf(StrictTruth(q.Truth)), lenient: setOf(q.Truth.Union())}
				for _, h := range hits[:min(ConfTopN, len(hits))] {
					l.Items = append(l.Items, ConfItem{Slug: h.Slug, Score: h.Score, Cov: qr.sig[h.Slug].Cov})
				}
				lists = append(lists, l)
			}
			ca := AnalyzeConfidence(lists, opt.Folds, name, opt.Seed)
			rep.Confidence = &ca
		}
	}

	// ---- cutoff variant: candidate notes dated after Q's day removed ----
	if !opt.NoCutoff {
		var lineupCut []lbScorer // LSA's space is fit on the full-notes corpus: skip it
		for _, l := range lineup {
			if !l.noCut {
				lineupCut = append(lineupCut, l)
			}
		}
		mc := NewModel(opt.Weights)
		visible := map[string]int{}
		for _, t := range tasks {
			mc.Upsert(t)
			visible[t.Slug] = len(t.Updates)
		}
		tcc := &termCache{m: mc}
		dayOf := func(ts string) string { return ts[:min(10, len(ts))] }
		var days []string
		seen := map[string]bool{}
		for _, mode := range LookbackModes {
			for _, qi := range testIdx(modeQs[mode]) {
				if d := dayOf(modeQs[mode][qi].Created); !seen[d] {
					seen[d] = true
					days = append(days, d)
				}
			}
		}
		sort.Strings(days)
		for _, mode := range LookbackModes {
			for _, l := range lineupCut {
				results[key{VarCutoff, mode, l.name}] = make([]*lbResult, len(modeQs[mode]))
			}
		}
		for _, d := range days {
			for _, t := range tasks {
				ups := NotesUpTo(t.Updates, d)
				if len(ups) != visible[t.Slug] {
					tt := t
					tt.Updates = ups
					mc.Upsert(tt)
					visible[t.Slug] = len(ups)
				}
			}
			tcc.t = map[string]map[string]bool{}
			for _, mode := range LookbackModes {
				ps := &PairSet{Model: mc}
				var sub []LBQuery
				var orig, all []int
				for qi, q := range modeQs[mode] {
					if q.Fold >= 1 && dayOf(q.Created) == d {
						ps.Queries = append(ps.Queries, newQueryRec(mc, tcc, q.Slug, lbQuery(mc, q.text), q.cands))
						all = append(all, len(sub))
						sub = append(sub, q)
						orig = append(orig, qi)
					}
				}
				if len(sub) == 0 {
					continue
				}
				for name, res := range score(lineupCut, ps, sub, all) {
					dst := results[key{VarCutoff, mode, name}]
					for i, oi := range orig {
						dst[oi] = res[i]
					}
				}
			}
		}
	}

	// ---- aggregate ----
	withPrompts := map[string]bool{}
	for _, q := range modeQs[ModePrompts] {
		withPrompts[q.Slug] = true
	}
	for _, variant := range []string{VarFull, VarCutoff} {
		for _, mode := range append(append([]string(nil), LookbackModes...), ModeBriefSubset) {
			resMode := mode
			if mode == ModeBriefSubset {
				resMode = ModeBrief // brief results, restricted to the brief+prompts query tasks
			}
			mq := modeQs[resMode]
			srcs := append([]string{"all", SrcNonProject}, TruthSources...)
			for _, src := range srcs {
				var ref, rel *LBRow
				var rows []LBRow
				for _, l := range lineup {
					res := results[key{variant, resMode, l.name}]
					if res == nil {
						continue
					}
					row := LBRow{Scorer: l.name, Variant: variant, Mode: mode, Source: src}
					for qi, r := range res {
						if r == nil || (mode == ModeBriefSubset && !withPrompts[mq[qi].Slug]) {
							continue
						}
						tr := truthFor(mq[qi].Truth, src)
						if len(tr) == 0 {
							continue
						}
						row.perQuery = append(row.perQuery, metricsOf(r.ranks, tr))
						row.qids = append(row.qids, qi)
					}
					if len(row.perQuery) == 0 {
						continue
					}
					rows = append(rows, row)
				}
				for i := range rows {
					if rows[i].Scorer == LookbackRef {
						ref = &rows[i]
					}
					if rows[i].Scorer == RelatedScorer {
						rel = &rows[i]
					}
				}
				for i := range rows {
					finishRow(&rows[i], ref, opt.Boot, opt.Seed)
					if rel != nil && rel != &rows[i] {
						rows[i].DMRRRel, rows[i].DMRRRelCI, rows[i].DR10Rel, rows[i].DR10RelCI = pairedDelta(&rows[i], rel, opt.Boot, opt.Seed)
					}
				}
				rep.Rows = append(rep.Rows, rows...)
			}
		}
	}

	// ---- best scorer, examples, misses ----
	best, bestMRR := "", -1.0
	for _, r := range rep.Rows {
		if r.Variant == VarFull && r.Mode == ModeBrief && r.Source == "all" && r.MRR > bestMRR {
			best, bestMRR = r.Scorer, r.MRR
		}
	}
	rep.Best = best
	bres := results[key{VarFull, ModeBrief, best}]
	bq := modeQs[ModeBrief]
	rep.Examples = examples(bq, bres, best, opt.Examples, opt.Seed)
	rep.Misses = misses(m, bps, bq, bres)

	// ---- leakage checks ----
	var leakScorers []Scorer
	stripped := map[string][]*lbResult{}
	for _, l := range lineup {
		if l.name == LookbackRef {
			leakScorers = append(leakScorers, l.fixed)
			stripped[l.name] = results[key{VarFull, ModeBrief, l.name}]
		}
	}
	rep.Leaks = leakChecks(m, tc, sm, tasks, byslug, bq, leakScorers, stripped, opt)
	rep.Notes = append(rep.Notes,
		"structure-derived truth (parent, project, family, playbook, tag) is never a scorer feature here",
		"cutoff variant skips LSA (its latent space is fit on the full-notes corpus)",
		"recall@k is capped at k/|truth| when a query has more than k truth items (projects average ~37); nDCG@10 is normalized for that, recall is not",
		"note dates come from update filenames (local day) and are compared to Q's UTC creation day; undated notes are kept")
	return rep
}

func truthCounts(truth map[string]TruthSet, qs []LBQuery) []SourceCount {
	inMode := map[string]map[string]bool{}
	for _, q := range qs {
		if inMode[q.Mode] == nil {
			inMode[q.Mode] = map[string]bool{}
		}
		inMode[q.Mode][q.Slug] = true
	}
	var out []SourceCount
	for _, src := range append([]string{"all"}, TruthSources...) {
		sc := SourceCount{Source: src}
		for q, ts := range truth {
			n := len(ts[src])
			if src == "all" {
				n = len(ts.Union())
			}
			if n == 0 {
				continue
			}
			sc.Queries++
			sc.Pairs += n
			if inMode[ModeBrief][q] {
				sc.QueriesBrief++
			}
			if inMode[ModePrompts][q] {
				sc.QueriesPr++
			}
			if src != "all" && len(ts) == 1 {
				sc.OnlySource++
			}
		}
		if sc.Queries > 0 {
			sc.MeanPerQ = float64(sc.Pairs) / float64(sc.Queries)
		}
		out = append(out, sc)
	}
	return out
}

func meanOf(xs []QueryMetrics, f func(QueryMetrics) float64, idx []int) float64 {
	if len(idx) == 0 {
		return 0
	}
	var s float64
	for _, i := range idx {
		s += f(xs[i])
	}
	return s / float64(len(idx))
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sort.Float64s(xs)
	i := int(math.Round(p * float64(len(xs)-1)))
	return xs[i]
}

// finishRow computes means, bootstrap CIs (resampling queries) and the
// paired delta vs ref on the queries both rows cover.
func finishRow(r *LBRow, ref *LBRow, boot int, seed uint64) {
	n := len(r.perQuery)
	r.Queries = n
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	mrr := func(m QueryMetrics) float64 { return m.MRR }
	r10 := func(m QueryMetrics) float64 { return m.R10 }
	r.R5 = meanOf(r.perQuery, func(m QueryMetrics) float64 { return m.R5 }, idx)
	r.R10 = meanOf(r.perQuery, r10, idx)
	r.R20 = meanOf(r.perQuery, func(m QueryMetrics) float64 { return m.R20 }, idx)
	r.MRR = meanOf(r.perQuery, mrr, idx)
	r.NDCG10 = meanOf(r.perQuery, func(m QueryMetrics) float64 { return m.NDCG10 }, idx)
	r.Hit5 = meanOf(r.perQuery, func(m QueryMetrics) float64 { return m.Hit5 }, idx)
	rng := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))
	bm, br := make([]float64, boot), make([]float64, boot)
	s := make([]int, n)
	for b := 0; b < boot; b++ {
		for i := range s {
			s[i] = rng.IntN(n)
		}
		bm[b], br[b] = meanOf(r.perQuery, mrr, s), meanOf(r.perQuery, r10, s)
	}
	r.MRRCI = CI{percentile(bm, .025), percentile(bm, .975)}
	r.R10CI = CI{percentile(br, .025), percentile(br, .975)}
	if ref == nil || ref == r {
		return
	}
	r.DMRRRef, r.DMRRCI, r.DR10Ref, r.DR10CI = pairedDelta(r, ref, boot, seed)
}

// pairedDelta is r − ref on MRR and R@10 over the queries both rows
// cover, with bootstrap 95% CIs.
func pairedDelta(r, ref *LBRow, boot int, seed uint64) (dMRR float64, ciMRR CI, dR10 float64, ciR10 CI) {
	refAt := map[int]QueryMetrics{}
	for i, q := range ref.qids {
		refAt[q] = ref.perQuery[i]
	}
	var dm, dr []float64
	for i, q := range r.qids {
		if x, ok := refAt[q]; ok {
			dm = append(dm, r.perQuery[i].MRR-x.MRR)
			dr = append(dr, r.perQuery[i].R10-x.R10)
		}
	}
	if len(dm) == 0 {
		return
	}
	avg := func(xs []float64, s []int) float64 {
		var t float64
		for _, i := range s {
			t += xs[i]
		}
		return t / float64(len(s))
	}
	all := make([]int, len(dm))
	for i := range all {
		all[i] = i
	}
	dMRR, dR10 = avg(dm, all), avg(dr, all)
	rng := rand.New(rand.NewPCG(seed, 0x51ed27))
	bm, br := make([]float64, boot), make([]float64, boot)
	s := make([]int, len(dm))
	for b := 0; b < boot; b++ {
		for i := range s {
			s[i] = rng.IntN(len(dm))
		}
		bm[b], br[b] = avg(dm, s), avg(dr, s)
	}
	return dMRR, CI{percentile(bm, .025), percentile(bm, .975)}, dR10, CI{percentile(br, .025), percentile(br, .975)}
}

// examples picks n test queries deterministically, preferring ones whose
// truth covers different sources, and lists the best scorer's top-5.
func examples(qs []LBQuery, res []*lbResult, scorer string, n int, seed uint64) []LBExample {
	var idx []int
	for qi, r := range res {
		if r != nil {
			idx = append(idx, qi)
		}
	}
	sort.Slice(idx, func(i, j int) bool {
		return slugHash(seed, qs[idx[i]].Slug) < slugHash(seed, qs[idx[j]].Slug)
	})
	var out []LBExample
	usedSrc := map[string]bool{}
	pick := func(qi int) {
		q := qs[qi]
		ex := LBExample{Query: q.Slug, Mode: q.Mode, Scorer: scorer, Truth: map[string][]string{}, Ranks: res[qi].ranks}
		for _, src := range TruthSources {
			for _, s := range q.Truth[src] {
				ex.Truth[s] = append(ex.Truth[s], src)
			}
		}
		for _, h := range res[qi].top {
			h.Relevant = ex.Truth[h.Slug]
			ex.Top5 = append(ex.Top5, h)
		}
		out = append(out, ex)
	}
	chosen := map[int]bool{}
	// pass 1: one query per primary source not yet shown
	for _, qi := range idx {
		if len(out) >= n {
			break
		}
		for _, src := range TruthSources {
			if len(qs[qi].Truth[src]) > 0 && !usedSrc[src] {
				usedSrc[src] = true
				chosen[qi] = true
				pick(qi)
				break
			}
		}
	}
	for _, qi := range idx {
		if len(out) >= n {
			break
		}
		if !chosen[qi] {
			chosen[qi] = true
			pick(qi)
		}
	}
	return out
}

func nameWords(m *Model, e *TaskEntry) map[string]bool {
	out := map[string]bool{}
	for id := range e.Fields[fName] {
		t := m.Terms[id]
		if !IsBigram(t) && !strings.Contains(t, "-") {
			out[t] = true
		}
	}
	return out
}

// misses buckets every truth item the best scorer left out of its top 20.
func misses(m *Model, ps *PairSet, qs []LBQuery, res []*lbResult) []MissCount {
	acc := map[string]*MissCount{}
	get := func(s string) *MissCount {
		if acc[s] == nil {
			acc[s] = &MissCount{Source: s}
		}
		return acc[s]
	}
	for qi, r := range res {
		if r == nil {
			continue
		}
		n := float64(len(m.Tasks))
		df := func(t string) float64 { return float64(m.dfOf(t)) / n }
		qterms := map[string]bool{}
		for t := range ps.Queries[qi].Q.tf {
			if !IsBigram(t) && !strings.Contains(t, "-") {
				qterms[t] = true
			}
		}
		srcOf := map[string][]string{}
		for _, src := range TruthSources {
			for _, s := range qs[qi].Truth[src] {
				srcOf[s] = append(srcOf[s], src)
			}
		}
		for c, srcs := range srcOf {
			bucket := ""
			if r.ranks[c] > 20 || r.ranks[c] == 0 {
				e := m.Tasks[c]
				size, shared, distinct, nameHit := 0, 0, 0, false
				if e != nil {
					for t := range unigramSet(e.termStrings(m)) {
						size++
						if qterms[t] && df(t) <= CommonDF {
							shared++
							if df(t) <= DistinctDF {
								distinct++
							}
						}
					}
					for t := range nameWords(m, e) {
						if qterms[t] && df(t) <= DistinctDF {
							nameHit = true
						}
					}
				}
				switch {
				case size < 5:
					bucket = "empty"
				case shared == 0:
					bucket = "none"
				case distinct == 0:
					bucket = "vocab"
				case !nameHit:
					bucket = "id"
				default:
					bucket = "out"
				}
			}
			for _, s := range append([]string{"all"}, srcs...) {
				mc := get(s)
				mc.Items++
				if bucket != "" {
					mc.Missed++
				}
				switch bucket {
				case "":
					mc.Top20++
				case "empty":
					mc.EmptyCand++
				case "none":
					mc.NoOverlap++
				case "vocab":
					mc.Vocabulary++
				case "id":
					mc.Identifier++
				case "out":
					mc.Outranked++
				}
			}
		}
	}
	var out []MissCount
	for _, s := range append([]string{"all"}, TruthSources...) {
		if acc[s] != nil {
			out = append(out, *acc[s])
		}
	}
	return out
}

// leakChecks measures the leaks the eval guards against.
func leakChecks(m *Model, tc *termCache, sm *SlugMatcher, tasks []TaskData, byslug map[string]TaskData,
	bq []LBQuery, scorers []Scorer, stripped map[string][]*lbResult, opt LookbackOptions) []LeakCheck {
	var out []LeakCheck
	// 1. residual slug mentions after stripping.
	resid, spans := 0, 0
	for _, q := range bq {
		if len(sm.Find(q.text)) > 0 {
			resid++
		}
		spans += q.Stripped
	}
	out = append(out, LeakCheck{"residual slug mentions in stripped queries", float64(resid),
		"queries (mode brief) still naming a slug after stripping; must be 0"})
	out = append(out, LeakCheck{"slug spans stripped (mode brief)", float64(spans), "total spans removed from query text"})
	// 2. time cutoff: candidates created strictly before Q.
	viol := 0
	for _, q := range bq {
		for _, c := range q.cands {
			if byslug[c].CreatedAt >= q.Created {
				viol++
			}
		}
	}
	out = append(out, LeakCheck{"candidates not created before Q", float64(viol), "must be 0"})
	// 3. unstripped vs stripped mention MRR (test queries, fixed scorers).
	ps := &PairSet{Model: m, Corpus: tasks}
	var keep []int
	for qi, q := range bq {
		if q.Fold < 1 || len(q.Truth[SrcMention]) == 0 {
			continue
		}
		raw, _ := queryText(byslug[q.Slug], ModeBrief, opt.PromptsN)
		ps.Queries = append(ps.Queries, newQueryRec(m, tc, q.Slug, lbQuery(m, raw), q.cands))
		keep = append(keep, qi)
	}
	for _, s := range scorers {
		bindTo(s, ps)
		var raw, str float64
		for i, qi := range keep {
			tr := bq[qi].Truth[SrcMention]
			r := resultOf(rankAll(s, ps, i, opt.Seed), tr)
			raw += metricsOf(r.ranks, tr).MRR
			if res := stripped[s.Name()]; res != nil && res[qi] != nil {
				str += metricsOf(res[qi].ranks, tr).MRR
			}
		}
		if n := float64(len(keep)); n > 0 {
			out = append(out, LeakCheck{s.Name() + " mention MRR, UNSTRIPPED vs stripped", raw / n,
				fmt.Sprintf("stripped (the eval's number) = %.4f on the same %d queries; the gap is what slug leakage would add", str/n, len(keep))})
		}
	}
	// 4. candidate notes dated after Q.
	late, total, undated := 0, 0, 0
	for _, q := range bq {
		day := q.Created[:min(10, len(q.Created))]
		for _, c := range q.cands {
			for _, u := range byslug[c].Updates {
				total++
				if u.Date == "" {
					undated++
				} else if u.Date > day {
					late++
				}
			}
		}
	}
	if total > 0 {
		out = append(out, LeakCheck{"candidate notes dated after Q (share)", float64(late) / float64(total),
			"over all (query, candidate note) pairs, mode brief; the notes<=Q variant drops them"})
		out = append(out, LeakCheck{"candidate notes undated (share)", float64(undated) / float64(total), "kept in both variants"})
	}
	return out
}

// NotesUpTo keeps the notes dated on or before day (YYYY-MM-DD); undated
// notes are kept.
func NotesUpTo(ups []Update, day string) []Update {
	var out []Update
	for _, u := range ups {
		if u.Date == "" || u.Date <= day {
			out = append(out, u)
		}
	}
	return out
}

// SrcNonProject is a report-only slice: every source except project
// (project truth is ~95% of all truth items and dominates "all").
const SrcNonProject = "non-project"

// Mention-truth slices (structure experiment): mention items in Q's
// project, and mention items outside it (cross-project relations).
const (
	SrcMentionSame  = "mention:same-project"
	SrcMentionCross = "mention:cross-project"
)

func truthFor(t TruthSet, src string) []string {
	switch src {
	case "all":
		return t.Union()
	case SrcMentionSame, SrcMentionCross:
		proj := map[string]bool{}
		for _, x := range t[SrcProject] {
			proj[x] = true
		}
		var out []string
		for _, x := range t[SrcMention] {
			if proj[x] == (src == SrcMentionSame) {
				out = append(out, x)
			}
		}
		return out
	case SrcNonProject:
		u := TruthSet{}
		for k, v := range t {
			if k != SrcProject {
				u[k] = v
			}
		}
		return u.Union()
	}
	return t[src]
}
