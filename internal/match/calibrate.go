package match

import "sort"

// Per-user calibration of the layer-3 confidence model.
//
// The shipped RelatedConf coefficients are fit once, on the author's flow
// history, and baked into the binary. `flow recall train --calibrate` refits
// the same logistic — p(relevant) over (score/self, log rank, gap-to-next,
// log query tokens) — on the running user's OWN links, using the shipped
// bm25+0.25lsa+card ranker and strict truth (mention / parent / family /
// playbook). The fit is persisted next to the cache and used in place of the
// default. It needs enough history to be meaningful; below MinCalibQueries
// truth-bearing queries calibration is refused.

// MinCalibQueries is the floor on truth-bearing training queries below which
// `recall train --calibrate` refuses to fit (too little signal to trust the
// logistic over the default). ~50 is where the per-fold base rates in the
// eval stop being dominated by a handful of links.
const MinCalibQueries = 50

// CalibrateRelated refits the layer-3 confidence model on tasks using the
// shipped ranker and strict truth. It returns the fit, the number of
// truth-bearing training queries it was fit on, and ok=false (without a usable
// fit) when that count is below MinCalibQueries. seed only affects the
// rankAll tie-break, matching the eval.
func CalibrateRelated(tasks []TaskData, seed uint64) (model ConfModel, nTruth int, ok bool) {
	opt := LookbackOptions{Weights: DefaultWeights, Seed: seed}
	qs, m, tc, strip := buildCalibQueries(tasks, opt)

	// One PairSet over every forward-chaining test query (fold >= 1), mode
	// brief — identical to RunLayers' confidence-fit path.
	ps := &PairSet{Model: m, Corpus: tasks}
	var test []int
	for i, q := range qs {
		if q.fold < 1 {
			continue
		}
		qr := newQueryRec(m, tc, q.slug, lbQuery(m, q.text), q.cands)
		qr.text = q.text // the card scorer re-extracts entity terms from the query
		ps.Queries = append(ps.Queries, qr)
		test = append(test, i)
	}

	cs := &CardScorer{Label: ShippedRelatedScorer, K: RelatedCardK, Norm: CardNormQuery, Lambda: 1, LSALambda: 0.25, Strip: strip}
	bindTo(cs, ps)
	var clists []*ConfList
	for ti, qi := range test {
		q := qs[qi]
		hits := rankAll(cs, ps, ti, seed)
		strict := StrictTruth(q.truth)
		if len(strict) > 0 {
			nTruth++
		}
		l := &ConfList{Query: q.slug, Fold: q.fold, Tokens: len(Tokenize(q.text)), Self: selfScore(cs, ps, ti),
			strict: setOf(strict), lenient: setOf(q.truth.Union())}
		qr := &ps.Queries[ti]
		for _, h := range hits[:min(ConfTopN, len(hits))] {
			l.Items = append(l.Items, ConfItem{Slug: h.Slug, Score: h.Score, Cov: qr.sig[h.Slug].Cov})
		}
		clists = append(clists, l)
	}
	if nTruth < MinCalibQueries {
		return ConfModel{}, nTruth, false
	}
	return ShipFit(clists, "strict"), nTruth, true
}

// calibQuery is a built mode-brief test query for the calibration refit.
type calibQuery struct {
	slug  string
	text  string
	cands []string
	truth TruthSet
	fold  int
}

// buildCalibQueries reproduces RunLookback's mode-brief query build and fold
// split exactly (same truth derivation, same slug stripping, same forward-
// chaining split). It returns every query carrying its fold; callers keep the
// fold >= 1 test queries. The returned Model, termCache and slug-strip closure
// are the index the calibration ranker binds to.
func buildCalibQueries(tasks []TaskData, opt LookbackOptions) ([]calibQuery, *Model, *termCache, func(string) string) {
	tasks = append([]TaskData(nil), tasks...)
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].CreatedAt != tasks[j].CreatedAt {
			return tasks[i].CreatedAt < tasks[j].CreatedAt
		}
		return tasks[i].Slug < tasks[j].Slug
	})
	var all []string
	for _, t := range tasks {
		all = append(all, t.Slug)
	}
	all = append(all, opt.StripSlugs...)
	sm := NewSlugMatcher(all)
	generic := GenericTags(tasks, opt.GenericTag)
	truth := DeriveTruth(tasks, sm, generic)

	var qs []calibQuery
	var times []string
	for _, q := range tasks {
		ts, ok := truth[q.Slug]
		if !ok {
			continue
		}
		raw, ok := queryText(q, ModeBrief, opt.PromptsN)
		if !ok {
			continue
		}
		text, _ := sm.Strip(raw)
		if len(Tokenize(text)) < MinQueryTokens {
			continue
		}
		var cands []string
		for _, c := range tasks {
			if c.CreatedAt < q.CreatedAt && c.Slug != q.Slug {
				cands = append(cands, c.Slug)
			}
		}
		qs = append(qs, calibQuery{slug: q.Slug, text: text, cands: cands, truth: ts})
		times = append(times, q.CreatedAt)
	}
	// forward-chaining fold split (identical to RunLookback).
	folds := opt.Folds
	if folds <= 0 {
		folds = EvalFolds
	}
	var starts []string
	if len(times) >= folds+1 {
		for i := 1; i <= folds; i++ {
			starts = append(starts, times[i*len(times)/(folds+1)])
		}
	}
	for i := range qs {
		created := times[i]
		f := 0
		for _, s := range starts {
			if created >= s {
				f++
			}
		}
		qs[i].fold = f
	}
	m := NewModel(opt.Weights)
	for _, t := range tasks {
		m.Upsert(t)
	}
	tc := &termCache{m: m, t: map[string]map[string]bool{}}
	strip := func(s string) string { out, _ := sm.Strip(s); return out }
	return qs, m, tc, strip
}
