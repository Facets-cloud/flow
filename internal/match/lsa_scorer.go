package match

import (
	"fmt"
	"math"
	"sort"
	"sync"
)

// LSAMode selects how an LSAScorer turns latent similarity into a verdict.
type LSAMode int

const (
	// LSAThreshold thresholds the raw latent kNN score (pure measure).
	LSAThreshold LSAMode = iota
	// LSAFusion ranks by BM25 + Lambda × latent score (both raw, no
	// learning): LSA as a secondary re-ranking signal. This is the shipped
	// mode — `bm25+0.25lsa` is LSAFusion with Lambda 0.25.
	LSAFusion
)

// LSAScorer scores a pair by the latent-space kNN similarity between the
// query and the target task's points (see lsa.go).
type LSAScorer struct {
	Label  string
	P      LSAParams
	M      int     // mean of the top-M point cosines
	Beta   float64 // blend weight of the centroid cosine
	Mode   LSAMode
	Lambda float64 // LSAFusion weight
	// Space, when set, is a pre-fit latent space used verbatim (its Pts
	// must cover ps.Corpus): the disk-cached space for `flow match
	// related`, so a live call skips the SVD. Reindex it before use.
	Space *LSASpace

	thr float64
	cur *lsaPrepared
}

type lsaPrepared struct {
	space  *LSASpace
	scores []map[string]float64 // per query: task -> latent score
	sorted [][]float64          // per query: candidate scores, descending
	top    []top2
}

func (s *LSAScorer) Name() string {
	if s.Label != "" {
		return s.Label
	}
	return fmt.Sprintf("lsa k%d a%.1f m%d", s.P.K, s.P.Alpha, s.M)
}

// lsaCache shares a fitted space between scorers with the same params on
// the same pair set (variants differing only in aggregation or mode).
var lsaCache struct {
	sync.Mutex
	keys []lsaKey
	vals []*LSASpace
}

type lsaKey struct {
	m *Model
	p LSAParams
}

// SpaceFor fits (or reuses) the LSA space over a pair set's corpus.
func SpaceFor(ps *PairSet, p LSAParams) *LSASpace {
	p = p.withDefaults()
	k := lsaKey{ps.Model, p}
	lsaCache.Lock()
	defer lsaCache.Unlock()
	for i, kk := range lsaCache.keys {
		if kk == k {
			return lsaCache.vals[i]
		}
	}
	var pts []LSAPoint
	for _, td := range ps.Corpus {
		pts = append(pts, PointsOf(td)...)
	}
	sp := FitLSA(pts, p)
	lsaCache.keys = append(lsaCache.keys, k)
	lsaCache.vals = append(lsaCache.vals, sp)
	if len(lsaCache.keys) > 8 {
		lsaCache.keys, lsaCache.vals = lsaCache.keys[1:], lsaCache.vals[1:]
	}
	return sp
}

// Prepare fits the space for ps and scores every query against every
// indexed task once. A pre-fit Space (disk cache) is used verbatim.
func (s *LSAScorer) Prepare(ps *PairSet) {
	sp := s.Space
	if sp == nil {
		sp = SpaceFor(ps, s.P)
	}
	s.cur = prepareWith(sp, ps, s.M, s.Beta)
}

func prepareWith(sp *LSASpace, ps *PairSet, m int, beta float64) *lsaPrepared {
	pr := &lsaPrepared{space: sp}
	pr.scores = make([]map[string]float64, len(ps.Queries))
	pr.sorted = make([][]float64, len(ps.Queries))
	pr.top = make([]top2, len(ps.Queries))
	for qi, qr := range ps.Queries {
		sc := sp.TaskScores(sp.Project(qr.Q.tf), m, beta)
		pr.scores[qi] = sc
		var xs []float64
		for _, c := range qr.Cands {
			v := sc[c]
			xs = append(xs, v)
			pr.top[qi].add(c, v)
		}
		sort.Sort(sort.Reverse(sort.Float64Slice(xs)))
		pr.sorted[qi] = xs
	}
	return pr
}

// latent returns the target's score, margin over the best competing
// candidate, and 1-based rank among the query's candidates.
func (pr *lsaPrepared) latent(p Pair) (v, margin float64, rank int) {
	v = pr.scores[p.Query][p.Target]
	xs := pr.sorted[p.Query]
	rank = 1 + sort.Search(len(xs), func(i int) bool { return xs[i] <= v })
	return v, v - pr.top[p.Query].other(p.Target), rank
}

// Fit picks the F1-optimal threshold on the raw score (both shipped modes
// rank by a raw measure; the threshold only affects the thresholded verdict,
// never the ranking recall uses).
func (s *LSAScorer) Fit(train []Pair) {
	scores := make([]float64, len(train))
	labels := make([]bool, len(train))
	for i, p := range train {
		scores[i], labels[i] = s.Score(p), p.Pos
	}
	s.thr = f1Threshold(scores, labels)
}

func (s *LSAScorer) Score(p Pair) float64 {
	v, _, _ := s.cur.latent(p)
	if s.Mode == LSAFusion {
		return p.A.BM25 + s.Lambda*v
	}
	return v // LSAThreshold: the raw latent score
}

func (s *LSAScorer) Belongs(p Pair) bool { return s.Score(p) >= s.thr }

// Threshold is the fitted decision cut on Score.
func (s *LSAScorer) Threshold() float64 { return s.thr }

// ---------- a small generic logistic layer ----------

type logit struct {
	mean, std, w []float64
	b, thr       float64
}

func (l logit) prob(x []float64) float64 {
	if l.w == nil {
		return 0.5
	}
	z := l.b
	for i := range x {
		z += l.w[i] * (x[i] - l.mean[i]) / l.std[i]
	}
	return 1 / (1 + math.Exp(-z))
}
