package match

import (
	"fmt"
	"math"
	"sort"
)

// ---------- confidence cut-off for the lookback list ----------
//
// Instead of always showing the top 10 related past tasks, show only the
// ones the scorer is confident about, capped at 3 or 5, and nothing at
// all when nothing clears the bar. Raw scores are not comparable across
// queries, so every rule is relative to something per-query:
//
//   relative   show item i if score_i >= T * score_top1
//   absolute   show item i if score_i >= T (the BM25 part of the fused
//              score is already divided by the query's max attainable)
//   self       show item i if score_i / self >= T, where self is the
//              query scored against its own task
//   elbow      cut at the biggest relative drop inside the top 10
//   calibrated a logistic p(relevant) over (score/self, log rank, gap to
//              the next item / self, log query length), fit on OLDER
//              queries (forward-chaining folds); show if p >= T
//   fixed      the plain top-Cap (baseline)
//
// Every rule keeps a prefix of the ranking (it stops at the first item
// that fails), then applies Cap and a Floor on score/self (so even the
// relative and elbow rules can show nothing).

// Cut-off rule kinds.
const (
	ConfFixed      = "fixed"
	ConfRelative   = "relative"
	ConfAbsolute   = "absolute"
	ConfSelf       = "self"
	ConfElbow      = "elbow"
	ConfCalibrated = "calibrated"
)

// Truth for the cut-off analysis.
const (
	ConfStrict  = "strict"  // mention / parent / family / playbook
	ConfLenient = "lenient" // strict + project / tag
)

// ConfTopN is how deep the cut-off rules look.
const ConfTopN = 10

// ConfCaps are the list caps tried (the brief asked for 3 and 5; 1 and 2
// show whether the cap itself is doing the work).
var ConfCaps = []int{1, 2, 3, 5}

// ConfItem is one ranked candidate.
type ConfItem struct {
	Slug  string  `json:"slug"`
	Score float64 `json:"score"`
	Cov   float64 `json:"cov"` // query coverage: IDF-weighted share of the query matched
}

// ConfList is one query's top-ConfTopN under the scorer, plus what the
// rules need to normalise it.
type ConfList struct {
	Query  string     `json:"query"`
	Fold   int        `json:"fold"`
	Tokens int        `json:"tokens"`
	Self   float64    `json:"self_score"`
	Items  []ConfItem `json:"items"`

	strict, lenient map[string]bool
	p               []float64 // calibrated p per item (set per truth)
}

// ConfRule is one cut-off rule at one operating point.
type ConfRule struct {
	Kind  string  `json:"kind"`
	T     float64 `json:"t"`
	Cap   int     `json:"cap"`
	Floor float64 `json:"floor"` // minimum score/self for any shown item
}

// String is a compact, human-readable rule name.
func (r ConfRule) String() string {
	fl := ""
	if r.Floor > 0 {
		fl = fmt.Sprintf(", floor %.2f", r.Floor)
	}
	switch r.Kind {
	case ConfFixed:
		return fmt.Sprintf("fixed top-%d", r.Cap)
	case ConfElbow:
		return fmt.Sprintf("elbow (cap %d%s)", r.Cap, fl)
	case ConfRelative:
		return fmt.Sprintf("relative ≥%.2f×top1 (cap %d%s)", r.T, r.Cap, fl)
	case ConfAbsolute:
		return fmt.Sprintf("absolute score ≥%.3f (cap %d%s)", r.T, r.Cap, fl)
	case ConfSelf:
		return fmt.Sprintf("score/self ≥%.3f (cap %d%s)", r.T, r.Cap, fl)
	case ConfCalibrated:
		return fmt.Sprintf("calibrated p≥%.2f (cap %d%s)", r.T, r.Cap, fl)
	}
	return r.Kind
}

// norm is score/self (0 when self is not positive).
func (l *ConfList) norm(i int) float64 {
	if l.Self <= 0 {
		return 0
	}
	return l.Items[i].Score / l.Self
}

// Shown returns how many leading items rule r shows for l.
func (r ConfRule) Shown(l *ConfList) int {
	n := len(l.Items)
	if r.Cap > 0 {
		n = min(n, r.Cap)
	}
	cut := n
	if r.Kind == ConfElbow {
		cut = min(n, elbowCut(l.Items))
	}
	for i := 0; i < cut; i++ {
		ok := true
		switch r.Kind {
		case ConfRelative:
			ok = l.Items[i].Score >= r.T*l.Items[0].Score && l.Items[0].Score > 0
		case ConfAbsolute:
			ok = l.Items[i].Score >= r.T
		case ConfSelf:
			ok = l.norm(i) >= r.T
		case ConfCalibrated:
			ok = i < len(l.p) && l.p[i] >= r.T
		}
		if !ok || (r.Floor > 0 && l.norm(i) < r.Floor) {
			return i
		}
	}
	return cut
}

// elbowCut is the prefix length before the biggest relative drop
// (s_i - s_{i+1}) / s_i within the list; the whole list when flat.
func elbowCut(items []ConfItem) int {
	best, cut := 0.0, len(items)
	for i := 0; i+1 < len(items); i++ {
		if items[i].Score <= 0 {
			break
		}
		if d := (items[i].Score - items[i+1].Score) / items[i].Score; d > best {
			best, cut = d, i+1
		}
	}
	return cut
}

// confFeatures are the calibrated rule's inputs for item i. The last, query
// coverage, is the IDF-weighted share of the query's informative terms this
// candidate matches: it lets confidence tell "#1 because it covers the whole
// query" from "#1 because it shares one strong word", so a single-term match
// (coverage ≈ 1/terms) is no longer called strong on rank alone.
func confFeatures(l *ConfList, i int) []float64 {
	gap := 0.0
	if i+1 < len(l.Items) && l.Self > 0 {
		gap = (l.Items[i].Score - l.Items[i+1].Score) / l.Self
	}
	return []float64{l.norm(i), math.Log(float64(i + 1)), gap, math.Log1p(float64(l.Tokens)), l.Items[i].Cov}
}

// ConfFeatureNames names confFeatures, in order.
var ConfFeatureNames = []string{"score/self", "log rank", "gap to next/self", "log query tokens", "query coverage"}

// fitProbLogit fits an UNWEIGHTED L2 logistic regression, so its output
// is a probability at the training base rate (fitLogit is class-balanced,
// which suits a ranking verdict but not "how often is this right").
func fitProbLogit(x [][]float64, y []bool) logit {
	var npos int
	for _, v := range y {
		if v {
			npos++
		}
	}
	if len(x) == 0 || npos == 0 || npos == len(x) {
		return logit{}
	}
	d := len(x[0])
	l := logit{mean: make([]float64, d), std: make([]float64, d), w: make([]float64, d)}
	n := float64(len(x))
	for i := 0; i < d; i++ {
		var s, ss float64
		for _, r := range x {
			s += r[i]
		}
		mu := s / n
		for _, r := range x {
			ss += (r[i] - mu) * (r[i] - mu)
		}
		sd := math.Sqrt(ss / n)
		if sd < 1e-9 {
			sd = 1
		}
		l.mean[i], l.std[i] = mu, sd
	}
	base := float64(npos) / n
	l.b = math.Log(base / (1 - base))
	const lr, l2, iters = 0.5, 1e-3, 800
	gw := make([]float64, d)
	for it := 0; it < iters; it++ {
		for i := range gw {
			gw[i] = 0
		}
		var gb float64
		for k, r := range x {
			e := l.prob(r)
			if y[k] {
				e--
			}
			for i := range gw {
				gw[i] += e * (r[i] - l.mean[i]) / l.std[i]
			}
			gb += e
		}
		for i := range l.w {
			l.w[i] -= lr * (gw[i]/n + l2*l.w[i])
		}
		l.b -= lr * gb / n
	}
	return l
}

// ConfCoef is one fold's calibrated-rule fit (standardised weights).
type ConfCoef struct {
	Truth string             `json:"truth"`
	Fold  int                `json:"fold"`
	Items int                `json:"train_items"`
	Base  float64            `json:"base_rate"`
	W     map[string]float64 `json:"weights"`
}

// calibrate sets l.p on every test list (Fold >= 1) from a logistic fit
// on the items of lists in older folds, forward-chaining like the eval.
func calibrate(lists []*ConfList, truth string, folds int) []ConfCoef {
	var out []ConfCoef
	for f := 1; f <= folds; f++ {
		var x [][]float64
		var y []bool
		for _, l := range lists {
			if l.Fold >= f {
				continue
			}
			rel := l.rel(truth)
			for i := range l.Items {
				x = append(x, confFeatures(l, i))
				y = append(y, rel[l.Items[i].Slug])
			}
		}
		lg := fitProbLogit(x, y)
		c := ConfCoef{Truth: truth, Fold: f, Items: len(x), W: map[string]float64{}}
		var np int
		for _, v := range y {
			if v {
				np++
			}
		}
		if len(y) > 0 {
			c.Base = float64(np) / float64(len(y))
		}
		for i, w := range lg.w {
			c.W[ConfFeatureNames[i]] = w
		}
		out = append(out, c)
		for _, l := range lists {
			if l.Fold != f {
				continue
			}
			l.p = make([]float64, len(l.Items))
			for i := range l.Items {
				if lg.w == nil {
					l.p[i] = c.Base
				} else {
					l.p[i] = lg.prob(confFeatures(l, i))
				}
			}
		}
	}
	return out
}

func (l *ConfList) rel(truth string) map[string]bool {
	if truth == ConfLenient {
		return l.lenient
	}
	return l.strict
}

// ConfStats is one rule's outcome over a set of queries.
type ConfStats struct {
	Rule       ConfRule `json:"rule"`
	Truth      string   `json:"truth"`
	Slice      string   `json:"slice"`
	Queries    int      `json:"queries"`
	Reachable  int      `json:"reachable"`       // queries with a relevant item in the top 10
	Empty      float64  `json:"empty"`           // share of queries showing nothing
	AvgShown   float64  `json:"avg_shown"`       // mean items shown (empties count as 0)
	AvgIfAny   float64  `json:"avg_if_any"`      // mean items shown when something is shown
	Precision  float64  `json:"precision"`       // relevant shown / shown ("how often a suggestion is right")
	Helps      float64  `json:"helps"`           // share of queries with >=1 relevant shown
	Missed     float64  `json:"missed"`          // of reachable queries, share that showed nothing
	HelpsReach float64  `json:"helps_reachable"` // of reachable queries, share with >=1 relevant shown
}

// ConfEval scores rule r over lists under truth.
func ConfEval(r ConfRule, lists []*ConfList, truth, slice string) ConfStats {
	st := ConfStats{Rule: r, Truth: truth, Slice: slice, Queries: len(lists)}
	var shown, right, empty, helps, missed, nonEmpty, helpsR int
	for _, l := range lists {
		rel := l.rel(truth)
		reach := false
		for _, it := range l.Items {
			reach = reach || rel[it.Slug]
		}
		n := r.Shown(l)
		hit := false
		for _, it := range l.Items[:n] {
			if rel[it.Slug] {
				right++
				hit = true
			}
		}
		shown += n
		if n == 0 {
			empty++
		} else {
			nonEmpty++
		}
		if hit {
			helps++
		}
		if reach {
			st.Reachable++
			if n == 0 {
				missed++
			}
			if hit {
				helpsR++
			}
		}
	}
	q := float64(max(1, len(lists)))
	st.Empty, st.AvgShown, st.Helps = float64(empty)/q, float64(shown)/q, float64(helps)/q
	if nonEmpty > 0 {
		st.AvgIfAny = float64(shown) / float64(nonEmpty)
	}
	if shown > 0 {
		st.Precision = float64(right) / float64(shown)
	}
	if st.Reachable > 0 {
		st.Missed = float64(missed) / float64(st.Reachable)
		st.HelpsReach = float64(helpsR) / float64(st.Reachable)
	}
	return st
}

// quantiles returns the given quantiles of xs (nearest rank), rounded.
func quantiles(xs []float64, ps []float64, digits float64) []float64 {
	if len(xs) == 0 {
		return nil
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	seen := map[float64]bool{}
	var out []float64
	for _, p := range ps {
		v := s[min(len(s)-1, int(p*float64(len(s))))]
		v = math.Round(v*digits) / digits
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// ConfGrid builds the rule grid. Thresholds for the absolute and self
// rules, and the floors, come from quantiles of the training lists'
// top-1 values so the grid spans the data whatever the score scale.
func ConfGrid(train []*ConfList) []ConfRule {
	var top1, top1n []float64
	for _, l := range train {
		if len(l.Items) > 0 {
			top1 = append(top1, l.Items[0].Score)
			top1n = append(top1n, l.norm(0))
		}
	}
	qs := []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9}
	absT := quantiles(top1, qs, 1000)
	selfT := quantiles(top1n, qs, 1000)
	floors := append([]float64{0}, quantiles(top1n, []float64{0.2, 0.4, 0.6}, 1000)...)
	var out []ConfRule
	for _, c := range []int{1, 2, 3, 5, 10} {
		out = append(out, ConfRule{Kind: ConfFixed, Cap: c})
	}
	for _, c := range ConfCaps {
		for _, fl := range floors {
			out = append(out, ConfRule{Kind: ConfElbow, Cap: c, Floor: fl})
			for r := 3; r <= 9; r++ {
				out = append(out, ConfRule{Kind: ConfRelative, T: float64(r) / 10, Cap: c, Floor: fl})
			}
		}
		for _, t := range absT {
			out = append(out, ConfRule{Kind: ConfAbsolute, T: t, Cap: c})
		}
		for _, t := range selfT {
			out = append(out, ConfRule{Kind: ConfSelf, T: t, Cap: c})
		}
		for p := 1; p <= 8; p++ {
			out = append(out, ConfRule{Kind: ConfCalibrated, T: float64(p) / 10, Cap: c})
		}
	}
	return out
}

// ConfPoint is a named operating point: chosen on the older half of the
// test queries, reported on the newer half and on all of them.
type ConfPoint struct {
	Name   string    `json:"name"`
	Goal   string    `json:"goal"`
	Rule   ConfRule  `json:"rule"`
	Tune   ConfStats `json:"tune"` // older test half (where it was chosen)
	Held   ConfStats `json:"held"` // newer test half
	All    ConfStats `json:"all"`  // every test query, strict truth
	AllLen ConfStats `json:"all_lenient"`
}

// Operating-point goals (strict truth, measured on the tuning half).
const (
	ConfCautiousPrec = 0.80 // suggestions right at least this often
	ConfBalancedPrec = 0.65
	ConfGenerousPrec = 0.50
)

// ConfAnalysis is the cut-off analysis in the lookback report.
type ConfAnalysis struct {
	Scorer       string        `json:"scorer"`
	Mode         string        `json:"mode"`
	Queries      int           `json:"test_queries"`
	TuneFolds    []int         `json:"tune_folds"`
	HeldFolds    []int         `json:"held_folds"`
	Stats        []ConfStats   `json:"stats"`         // every rule, all test queries, both truths
	Points       []ConfPoint   `json:"points"`        // cautious, balanced, generous
	SimplePoints []ConfPoint   `json:"simple_points"` // same, rules without a fitted model
	Recommended  ConfRule      `json:"recommended"`   // the rule `flow match related` ships
	Ship         ConfModel     `json:"shipped_fit"`   // calibrated fit on every query
	Strong       ConfRule      `json:"strong"`        // items it also passes are labelled strong
	Examples     []ConfExample `json:"examples"`
	Coefs        []ConfCoef    `json:"calibrated_fits"`
	Notes        []string      `json:"notes"`
}

// ConfExample is what the user would see for one task.
type ConfExample struct {
	Query string         `json:"query"`
	Shown []ConfShown    `json:"shown"`
	Truth map[string]int `json:"relevant_in_top10"` // relevant slug -> rank (strict)
}

// ConfShown is one shown item with its label.
type ConfShown struct {
	Slug     string `json:"slug"`
	Label    string `json:"label"`
	Relevant bool   `json:"relevant"`
}

// simple rules need no fitted model at run time.
func simple(r ConfRule) bool { return r.Kind != ConfCalibrated }

// ConfGoals are the three operating points (strict truth): the most
// helpful rule whose suggestions are right at least this often.
var ConfGoals = []struct {
	Name string
	Prec float64
}{{"Cautious", ConfCautiousPrec}, {"Balanced", ConfBalancedPrec}, {"Generous", ConfGenerousPrec}}

// pickRule returns the rule in grid (passing ok) with the most helps on
// lists whose precision reaches goal; ties go to fewer items shown.
func pickRule(grid []ConfRule, lists []*ConfList, truth string, goal float64, ok func(ConfRule) bool) (ConfRule, bool) {
	var best ConfStats
	found := false
	for _, r := range grid {
		if !ok(r) {
			continue
		}
		s := ConfEval(r, lists, truth, "")
		if s.Precision < goal {
			continue
		}
		if !found || s.Helps > best.Helps+1e-9 ||
			(math.Abs(s.Helps-best.Helps) <= 1e-9 && s.AvgShown < best.AvgShown-1e-9) {
			best, found = s, true
		}
	}
	return best.Rule, found
}

// AnalyzeConfidence runs the cut-off analysis over brief-mode lists
// (every fold; Fold 0 only trains the calibrated rule).
func AnalyzeConfidence(lists []*ConfList, folds int, scorer string, seed uint64) ConfAnalysis {
	a := ConfAnalysis{Scorer: scorer, Mode: ModeBrief}
	var test, older, newer, train0 []*ConfList
	half := (folds + 1) / 2
	for f := 1; f <= folds; f++ {
		if f <= half {
			a.TuneFolds = append(a.TuneFolds, f)
		} else {
			a.HeldFolds = append(a.HeldFolds, f)
		}
	}
	for _, l := range lists {
		switch {
		case l.Fold == 0:
			train0 = append(train0, l)
		case l.Fold <= half:
			older = append(older, l)
			test = append(test, l)
		default:
			newer = append(newer, l)
			test = append(test, l)
		}
	}
	a.Queries = len(test)
	grid := ConfGrid(train0)
	points := func(ok func(ConfRule) bool, tag string) []ConfPoint {
		var out []ConfPoint
		for _, g := range ConfGoals {
			r, found := pickRule(grid, test, ConfStrict, g.Prec, ok)
			if !found {
				a.Notes = append(a.Notes, fmt.Sprintf("%s (%s): no rule is right %.0f%% of the time", g.Name, tag, 100*g.Prec))
				continue
			}
			out = append(out, ConfPoint{Name: g.Name, Goal: fmt.Sprintf("right ≥%.0f%% of the time", 100*g.Prec), Rule: r,
				Tune: ConfEval(r, older, ConfStrict, "older"), Held: ConfEval(r, newer, ConfStrict, "newer"),
				All: ConfEval(r, test, ConfStrict, "all"), AllLen: ConfEval(r, test, ConfLenient, "all")})
		}
		return out
	}
	// lenient first, strict last: l.p stays on the strict fit afterwards.
	for _, truth := range []string{ConfLenient, ConfStrict} {
		a.Coefs = append(a.Coefs, calibrate(lists, truth, folds)...)
		for _, r := range grid {
			a.Stats = append(a.Stats, ConfEval(r, test, truth, "all"))
		}
	}
	a.Points = points(func(ConfRule) bool { return true }, "any rule")
	a.SimplePoints = points(simple, "simple rules")
	for _, p := range a.Points {
		switch p.Name {
		case "Balanced":
			a.Recommended = p.Rule
		case "Cautious":
			a.Strong = p.Rule
		}
	}
	// a strong label must come from the same kind of rule as the
	// recommended one, so the two labels nest.
	if a.Strong.Kind != a.Recommended.Kind {
		a.Strong, _ = pickRule(grid, test, ConfStrict, ConfCautiousPrec, func(r ConfRule) bool { return r.Kind == a.Recommended.Kind })
	}
	a.Ship = ShipFit(lists, ConfStrict)
	a.Examples = confExamples(test, a.Recommended, a.Strong, seed)
	a.Notes = append(a.Notes,
		"strict truth = mention, parent, family, playbook; lenient adds project and tag",
		"operating points: the most helpful rule that is right at least X% of the time on all test queries (one threshold per rule, so little room to overfit); the older/newer columns check it holds over time",
		"absolute/self thresholds and floors are quantiles of the fold-0 (train-only) top-1 values",
		"calibrated: unweighted logistic on the top-10 items of OLDER queries only (forward-chaining), so p is a real hit rate; the shipped fit uses every query")
	return a
}

// Label returns "strong" / "possible" / "" for item i of l.
func ConfLabel(l *ConfList, i int, rec, strong ConfRule) string {
	if strong.Kind != "" && i < strong.Shown(l) {
		return "strong"
	}
	if i < rec.Shown(l) {
		return "possible"
	}
	return ""
}

// confExamples picks up to 3 test queries (seeded): one where the
// recommended rule shows a relevant item, one where it shows only misses
// or a mix, and one where it stays silent.
func confExamples(test []*ConfList, rec, strong ConfRule, seed uint64) []ConfExample {
	if rec.Kind == "" {
		return nil
	}
	order := append([]*ConfList(nil), test...)
	sort.SliceStable(order, func(i, j int) bool { return slugHash(seed, order[i].Query) < slugHash(seed, order[j].Query) })
	want := []func(n, hits int) bool{
		func(n, hits int) bool { return n > 0 && hits == n },
		func(n, hits int) bool { return n > 0 && hits < n },
		func(n, hits int) bool { return n == 0 },
	}
	var out []ConfExample
	for _, w := range want {
		for _, l := range order {
			n := rec.Shown(l)
			hits := 0
			for _, it := range l.Items[:n] {
				if l.strict[it.Slug] {
					hits++
				}
			}
			if !w(n, hits) {
				continue
			}
			ex := ConfExample{Query: l.Query, Truth: map[string]int{}}
			for i, it := range l.Items {
				if l.strict[it.Slug] {
					ex.Truth[it.Slug] = i + 1
				}
			}
			for i := 0; i < n; i++ {
				ex.Shown = append(ex.Shown, ConfShown{Slug: l.Items[i].Slug, Label: ConfLabel(l, i, rec, strong), Relevant: l.strict[l.Items[i].Slug]})
			}
			out = append(out, ex)
			break
		}
	}
	return out
}

// selfScore scores query qi against its own task (the query owner) with
// a raw scorer (bm25 / LSA fusion): the per-query normaliser.
func selfScore(s Scorer, ps *PairSet, qi int) float64 {
	qr := &ps.Queries[qi]
	sg := ps.Model.SignalsFor(qr.Q, qr.Owner)
	a := Assessment{Target: qr.Owner, Tokens: qr.Q.Tokens, Rank: 1, Sim: sg.Sim, KNN: sg.KNN, Cov: sg.Cov, BM25: sg.BM25}
	return math.Round(s.Score(Pair{Query: qi, Target: qr.Owner, A: a})*1e9) / 1e9
}

// StrictSources are the truth sources the cut-off analysis counts as
// "relevant" by default (project and tag are the lenient extras).
var StrictSources = []string{SrcMention, SrcParent, SrcFamily, SrcPlaybook}

// StrictTruth is the union of t's strict sources.
func StrictTruth(t TruthSet) []string {
	set := map[string]bool{}
	for _, src := range StrictSources {
		for _, x := range t[src] {
			set[x] = true
		}
	}
	return sortedKeys(set)
}

func setOf(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// ConfModel is a calibrated-rule fit in shippable form.
type ConfModel struct {
	Mean []float64 `json:"mean"`
	Std  []float64 `json:"std"`
	W    []float64 `json:"w"`
	B    float64   `json:"b"`
}

// Prob is p(relevant) for item i of l.
func (c ConfModel) Prob(l *ConfList, i int) float64 {
	return logit{mean: c.Mean, std: c.Std, w: c.W, b: c.B}.prob(confFeatures(l, i))
}

// ShipFit fits the calibrated rule on every list's top-10 items.
func ShipFit(lists []*ConfList, truth string) ConfModel {
	var x [][]float64
	var y []bool
	for _, l := range lists {
		rel := l.rel(truth)
		for i := range l.Items {
			x = append(x, confFeatures(l, i))
			y = append(y, rel[l.Items[i].Slug])
		}
	}
	lg := fitProbLogit(x, y)
	return ConfModel{Mean: lg.mean, Std: lg.std, W: lg.w, B: lg.b}
}
