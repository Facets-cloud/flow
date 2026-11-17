package match

import (
	"math"
	"math/rand/v2"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// ---------- LSA (latent semantic analysis) experiment ----------
//
// Each task contributes several *points* — its name, each brief section,
// each update note, each past prompt — instead of one concatenated
// document. Points are TF-IDF vectors (IDF over points), reduced with a
// truncated SVD (randomized range finder + power iterations, pure Go)
// into a dense k-dim space where words that co-occur across points share
// directions. A query is folded into the same space with the learned
// right singular vectors, and a task scores by the mean cosine of its
// nearest points (kNN), so a long multi-topic task is judged by its best
// matching section rather than a diluted centroid.

// LSAParams configures one LSA space.
type LSAParams struct {
	K       int     // latent dimensions
	Alpha   float64 // coordinates = U Σ^Alpha (1 = classic LSI, 0 = whitened)
	MinDF   int     // drop terms in fewer than MinDF points
	Bigrams bool    // keep Counts' bigram terms
	Power   int     // power iterations of the randomized SVD
	Over    int     // oversampling columns
	Seed    uint64
}

// DefaultLSAParams is the shipped latent-space configuration. K was 200 on
// the original eval; it is held at 128 as a memory/quality trade — the
// latent basis V (vocab×K) and per-point coordinates (points×K) shrink ~36%,
// while the lookback eval moves only within noise (bm25+0.25lsa MRR 0.886→0.880,
// R@10 0.511→0.505, the paired ΔMRR-vs-bm25 CI still excluding 0). Alpha/MinDF
// remain the best variants measured on the real-history eval.
var DefaultLSAParams = LSAParams{K: 128, Alpha: 0.5, MinDF: 2, Power: 2, Over: 20, Seed: 1}

func (p LSAParams) withDefaults() LSAParams {
	d := DefaultLSAParams
	if p.K <= 0 {
		p.K = d.K
	}
	if p.MinDF <= 0 {
		p.MinDF = d.MinDF
	}
	if p.Power < 0 {
		p.Power = 0
	}
	if p.Over <= 0 {
		p.Over = d.Over
	}
	if p.Seed == 0 {
		p.Seed = d.Seed
	}
	return p
}

// Point kinds.
const (
	PtName = iota
	PtBrief
	PtUpdate
	PtPrompt
)

// LSAPoint is one indexed text unit of a task.
type LSAPoint struct {
	Slug   string
	Kind   int
	Counts map[string]float64
}

// briefSections splits markdown on heading lines; each heading starts a
// section (heading text included, so "## Billing" stays with its body).
func briefSections(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, t)
		}
		cur.Reset()
	}
	for _, ln := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "#") {
			flush()
		}
		cur.WriteString(ln)
		cur.WriteByte('\n')
	}
	flush()
	return out
}

// PointsOf turns a task into its LSA points: name+slug, brief sections,
// update notes and informative prompts. Points under MinQueryTokens
// content tokens are dropped (except the name, which is always kept).
func PointsOf(td TaskData) []LSAPoint {
	var out []LSAPoint
	add := func(kind int, text string, limit int) {
		c := Counts(text, limit)
		if kind != PtName && sumCounts(c) < MinQueryTokens {
			return
		}
		if len(c) > 0 {
			out = append(out, LSAPoint{td.Slug, kind, c})
		}
	}
	add(PtName, td.Name+" "+td.Slug, 0)
	for _, sec := range briefSections(td.Brief) {
		add(PtBrief, sec, 0)
	}
	for _, u := range td.Updates {
		add(PtUpdate, u.Text, 0)
	}
	ps := td.Prompts
	if len(ps) > MaxKNNPrompts {
		ps = ps[len(ps)-MaxKNNPrompts:]
	}
	for _, p := range ps {
		add(PtPrompt, p, MaxPromptTokens)
	}
	return out
}

type spvec struct {
	idx []int32
	val []float64
}

// LSASpace is a fitted latent space plus every point's coordinates.
//
// V, Coord and IDF are stored as float32 (not float64): the latent basis
// and the per-point coordinates are the bulk of the warm index, and a
// truncated SVD carries nothing like 15 significant digits of signal, so
// halving their width halves that memory while moving cosines by < 1e-6.
// All accumulation (Project, TaskScores) still runs in float64 — only the
// stored matrices are narrowed.
type LSASpace struct {
	P      LSAParams
	Vocab  map[string]int32
	IDF    []float32
	Sigma  []float64   // singular values, descending
	V      [][]float32 // per term: its k-dim right-singular row, pre-scaled by Σ^(Alpha-1)
	Pts    []LSAPoint
	Coord  [][]float32 // per point: unit-norm latent coordinates
	Bytes  int         // approximate in-memory size of V + Coord + IDF
	byTask map[string][]int
	terms  []string // lazy inverse of Vocab
}

// Rebase returns a space with the same fitted basis, vocabulary and IDF
// but no points — the starting point for an incremental (fold-in) index.
func (s *LSASpace) Rebase() *LSASpace {
	return &LSASpace{P: s.P, Vocab: s.Vocab, IDF: s.IDF, Sigma: s.Sigma, V: s.V,
		Bytes: 4 * (len(s.V)*len(s.Sigma) + len(s.IDF))}
}

func (s *LSASpace) vectorize(c map[string]float64) spvec {
	var v spvec
	for t, k := range c {
		if !s.P.Bigrams && IsBigram(t) {
			continue
		}
		if i, ok := s.Vocab[t]; ok {
			v.idx = append(v.idx, i)
			v.val = append(v.val, (1+math.Log(k))*float64(s.IDF[i]))
		}
	}
	var ss float64
	for _, x := range v.val {
		ss += x * x
	}
	if ss > 0 {
		n := math.Sqrt(ss)
		for i := range v.val {
			v.val[i] /= n
		}
	}
	return v
}

// Project folds term counts into the latent space (unit norm; nil when
// no in-vocabulary term survives). This is the fold-in used both for
// queries and for incremental points: x·V_k, with no refit.
func (s *LSASpace) Project(c map[string]float64) []float64 {
	v := s.vectorize(c)
	if len(v.idx) == 0 {
		return nil
	}
	k := len(s.Sigma)
	out := make([]float64, k)
	for j, i := range v.idx {
		row, x := s.V[i], v.val[j]
		for d := 0; d < k; d++ {
			out[d] += x * float64(row[d])
		}
	}
	if unit(out) == 0 {
		return nil
	}
	return out
}

func unit(x []float64) float64 {
	var ss float64
	for _, v := range x {
		ss += v * v
	}
	if ss == 0 {
		return 0
	}
	n := math.Sqrt(ss)
	for i := range x {
		x[i] /= n
	}
	return n
}

func dot(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// dotf is dot(a, b) for a float32-stored b (a point's latent coordinate),
// accumulating in float64.
func dotf(a []float64, b []float32) float64 {
	var s float64
	for i := range a {
		s += a[i] * float64(b[i])
	}
	return s
}

// FitLSA builds the vocabulary and IDF over points and computes a rank-K
// truncated SVD of the point × term TF-IDF matrix.
func FitLSA(pts []LSAPoint, p LSAParams) *LSASpace {
	p = p.withDefaults()
	s := &LSASpace{P: p, Vocab: map[string]int32{}}
	df := map[string]int{}
	for _, pt := range pts {
		for t := range pt.Counts {
			if p.Bigrams || !IsBigram(t) {
				df[t]++
			}
		}
	}
	terms := make([]string, 0, len(df))
	for t, n := range df {
		if n >= p.MinDF {
			terms = append(terms, t)
		}
	}
	sort.Strings(terms)
	N := float64(len(pts))
	s.IDF = make([]float32, len(terms))
	for i, t := range terms {
		s.Vocab[t] = int32(i)
		s.IDF[i] = float32(math.Log((N+1)/(float64(df[t])+1)) + 1)
	}
	rows := make([]spvec, len(pts))
	for i, pt := range pts {
		rows[i] = s.vectorize(pt.Counts)
	}
	sigma, vt := randomizedSVD(rows, len(terms), p)
	k := len(sigma)
	s.Sigma = sigma
	s.V = make([][]float32, len(terms))
	scale := make([]float64, k)
	for d := 0; d < k; d++ {
		scale[d] = math.Pow(sigma[d], p.Alpha-1)
	}
	for t := range s.V {
		r := make([]float32, k)
		for d := 0; d < k; d++ {
			r[d] = float32(vt[d][t] * scale[d])
		}
		s.V[t] = r
	}
	s.Bytes = 4 * (len(terms)*k + len(terms))
	for _, pt := range pts {
		s.AddPoint(pt)
	}
	return s
}

// AddPoint folds one new point into the space without refitting (the
// incremental path). Points whose terms are all out of vocabulary are
// kept with a nil coordinate and never match.
func (s *LSASpace) AddPoint(pt LSAPoint) {
	if s.byTask == nil {
		s.byTask = map[string][]int{}
	}
	s.byTask[pt.Slug] = append(s.byTask[pt.Slug], len(s.Pts))
	s.Pts = append(s.Pts, pt)
	c := s.Project(pt.Counts)
	var cf []float32
	if c != nil {
		cf = make([]float32, len(c))
		for i, x := range c {
			cf[i] = float32(x)
		}
	}
	s.Coord = append(s.Coord, cf)
	s.Bytes += 4 * len(cf)
}

// NumPoints is how many points a task has in the space.
func (s *LSASpace) NumPoints(slug string) int { return len(s.byTask[slug]) }

// Reindex rebuilds the per-task point index (byTask) from Pts. It MUST be
// called after a space is restored from disk (gob drops the unexported
// byTask map), before any TaskScores/Project call.
func (s *LSASpace) Reindex() {
	s.byTask = make(map[string][]int, len(s.Pts))
	for i, pt := range s.Pts {
		s.byTask[pt.Slug] = append(s.byTask[pt.Slug], i)
	}
}

// Compact drops data a read-only, no-new-points space never reads:
// each point's raw term Counts (used only when folding in NEW points via
// AddPoint) and the right-singular V rows (used only to Project new
// documents). Scoring an already-indexed task against a projected query
// uses only Coord, byTask, Vocab, IDF and Sigma. A compacted space can
// still Project queries? No — Project needs V. So Compact keeps V and
// drops only Pts.Counts, which Project/TaskScores never touch. The result
// is a much smaller on-disk cache; call Reindex after restoring it.
func (s *LSASpace) Compact() {
	for i := range s.Pts {
		s.Pts[i].Counts = nil
	}
}

// TaskScores scores a projected query against every task: the mean of
// the task's top-m point cosines (fewer when the task has fewer points),
// blended with the cosine to the task's point centroid by beta.
func (s *LSASpace) TaskScores(q []float64, m int, beta float64) map[string]float64 {
	out := make(map[string]float64, len(s.byTask))
	if q == nil {
		for slug := range s.byTask {
			out[slug] = 0
		}
		return out
	}
	if m <= 0 {
		m = 1
	}
	best := make([]float64, m)
	cent := make([]float64, len(q))
	for slug, ids := range s.byTask {
		for i := range best {
			best[i] = math.Inf(-1)
		}
		for i := range cent {
			cent[i] = 0
		}
		n := 0
		for _, id := range ids {
			c := s.Coord[id]
			if c == nil {
				continue
			}
			n++
			v := dotf(q, c)
			for i := range cent {
				cent[i] += float64(c[i])
			}
			for i := range best {
				if v > best[i] {
					copy(best[i+1:], best[i:m-1])
					best[i] = v
					break
				}
			}
		}
		if n == 0 {
			out[slug] = 0
			continue
		}
		top := m
		if n < top {
			top = n
		}
		var sum float64
		for _, b := range best[:top] {
			sum += b
		}
		sc := sum / float64(top)
		if beta > 0 && unit(cent) > 0 {
			sc = (1-beta)*sc + beta*dot(q, cent)
		}
		out[slug] = sc
	}
	return out
}

// ---------- randomized truncated SVD (Halko, Martinsson & Tropp) ----------

// randomizedSVD returns the top singular values of the n×V sparse matrix
// A (rows) and the matching right singular vectors as k rows of length V.
func randomizedSVD(rows []spvec, nterms int, p LSAParams) ([]float64, [][]float64) {
	n := len(rows)
	l := p.K + p.Over
	if l > nterms {
		l = nterms
	}
	if l > n {
		l = n
	}
	if l == 0 {
		return nil, nil
	}
	rng := rand.New(rand.NewPCG(p.Seed, 0x15a))
	omega := make([][]float64, nterms)
	for t := range omega {
		r := make([]float64, l)
		for j := range r {
			r[j] = rng.NormFloat64()
		}
		omega[t] = r
	}
	y := mulA(rows, omega, l)
	for it := 0; it < p.Power; it++ {
		y = orthonormalize(y)
		z := orthonormalize(mulAT(rows, y, nterms))
		y = mulA(rows, z, len(z[0]))
	}
	q := orthonormalize(y)
	bt := mulAT(rows, q, nterms) // V × l' = (QᵀA)ᵀ
	lp := len(q[0])
	g := gram(bt, lp) // B Bᵀ
	lam, w := jacobiEigen(g)
	k := p.K
	if k > lp {
		k = lp
	}
	var sigma []float64
	for d := 0; d < k && lam[d] > 1e-12*lam[0]; d++ {
		sigma = append(sigma, math.Sqrt(lam[d]))
	}
	k = len(sigma)
	wk := make([][]float64, lp) // lp × k: W_k Σ⁻¹
	for j := range wk {
		wk[j] = make([]float64, k)
		for d := 0; d < k; d++ {
			wk[j][d] = w[j][d] / sigma[d]
		}
	}
	vrows := mulDense(bt, wk) // nterms × k
	vt := make([][]float64, k)
	for d := range vt {
		vt[d] = make([]float64, nterms)
		for t := range vrows {
			vt[d][t] = vrows[t][d]
		}
	}
	return sigma, vt
}

func parallel(n int, f func(lo, hi int)) {
	w := runtime.GOMAXPROCS(0)
	if w > 8 {
		w = 8
	}
	if n < 256 || w == 1 {
		f(0, n)
		return
	}
	var wg sync.WaitGroup
	step := (n + w - 1) / w
	for lo := 0; lo < n; lo += step {
		hi := lo + step
		if hi > n {
			hi = n
		}
		wg.Add(1)
		go func(lo, hi int) { defer wg.Done(); f(lo, hi) }(lo, hi)
	}
	wg.Wait()
}

// mulA computes A·X (n × w) for X given as V rows of width w.
func mulA(rows []spvec, x [][]float64, w int) [][]float64 {
	out := make([][]float64, len(rows))
	parallel(len(rows), func(lo, hi int) {
		for i := lo; i < hi; i++ {
			r := make([]float64, w)
			for j, t := range rows[i].idx {
				v, xr := rows[i].val[j], x[t]
				for d := 0; d < w; d++ {
					r[d] += v * xr[d]
				}
			}
			out[i] = r
		}
	})
	return out
}

// mulAT computes Aᵀ·Y (V × w) for Y given as n rows of width w.
func mulAT(rows []spvec, y [][]float64, nterms int) [][]float64 {
	w := len(y[0])
	out := make([][]float64, nterms)
	for t := range out {
		out[t] = make([]float64, w)
	}
	for i, r := range rows {
		yr := y[i]
		for j, t := range r.idx {
			v, o := r.val[j], out[t]
			for d := 0; d < w; d++ {
				o[d] += v * yr[d]
			}
		}
	}
	return out
}

// gram returns XᵀX (w × w) for X given as rows of width w.
func gram(x [][]float64, w int) [][]float64 {
	g := make([][]float64, w)
	for i := range g {
		g[i] = make([]float64, w)
	}
	var mu sync.Mutex
	parallel(len(x), func(lo, hi int) {
		loc := make([][]float64, w)
		for i := range loc {
			loc[i] = make([]float64, w)
		}
		for _, r := range x[lo:hi] {
			for a := 0; a < w; a++ {
				ra, la := r[a], loc[a]
				if ra == 0 {
					continue
				}
				for b := a; b < w; b++ {
					la[b] += ra * r[b]
				}
			}
		}
		mu.Lock()
		for a := 0; a < w; a++ {
			for b := a; b < w; b++ {
				g[a][b] += loc[a][b]
			}
		}
		mu.Unlock()
	})
	for a := 0; a < w; a++ {
		for b := 0; b < a; b++ {
			g[a][b] = g[b][a]
		}
	}
	return g
}

// orthonormalize returns an orthonormal basis for the column span of X
// (rows of width w): CholeskyQR2 (X ← X R⁻¹ twice, R = chol(XᵀX)), which
// is cheap for tall-skinny X. If X is numerically rank deficient the
// Cholesky breaks down and it falls back to an eigendecomposition of XᵀX
// that drops null directions.
func orthonormalize(x [][]float64) [][]float64 {
	for pass := 0; pass < 2; pass++ {
		w := len(x[0])
		rinv, ok := cholInvUpper(gram(x, w))
		if !ok {
			return orthonormalizeEig(x)
		}
		x = mulDense(x, rinv)
	}
	return x
}

// cholInvUpper factors G = RᵀR and returns R⁻¹ (upper triangular), or
// false when G is not numerically positive definite.
func cholInvUpper(g [][]float64) ([][]float64, bool) {
	n := len(g)
	var maxd float64
	for i := 0; i < n; i++ {
		maxd = math.Max(maxd, g[i][i])
	}
	r := make([][]float64, n)
	for i := range r {
		r[i] = make([]float64, n)
	}
	for j := 0; j < n; j++ {
		s := g[j][j]
		for k := 0; k < j; k++ {
			s -= r[k][j] * r[k][j]
		}
		if s <= 1e-12*maxd {
			return nil, false
		}
		r[j][j] = math.Sqrt(s)
		for i := j + 1; i < n; i++ {
			t := g[j][i]
			for k := 0; k < j; k++ {
				t -= r[k][j] * r[k][i]
			}
			r[j][i] = t / r[j][j]
		}
	}
	inv := make([][]float64, n)
	for i := range inv {
		inv[i] = make([]float64, n)
	}
	for j := 0; j < n; j++ { // solve R · inv[:,j] = e_j by back substitution
		for i := j; i >= 0; i-- {
			t := 0.0
			if i == j {
				t = 1
			}
			for k := i + 1; k <= j; k++ {
				t -= r[i][k] * inv[k][j]
			}
			inv[i][j] = t / r[i][i]
		}
	}
	return inv, true
}

func orthonormalizeEig(x [][]float64) [][]float64 {
	for pass := 0; pass < 2; pass++ {
		w := len(x[0])
		lam, v := jacobiEigen(gram(x, w))
		var keep []int
		for d := range lam {
			if lam[d] > 1e-10*lam[0] && lam[d] > 0 {
				keep = append(keep, d)
			}
		}
		m := make([][]float64, w) // w × len(keep): V Λ^{-1/2}
		for a := 0; a < w; a++ {
			m[a] = make([]float64, len(keep))
			for j, d := range keep {
				m[a][j] = v[a][d] / math.Sqrt(lam[d])
			}
		}
		x = mulDense(x, m)
	}
	return x
}

func mulDense(x, m [][]float64) [][]float64 {
	w2 := len(m[0])
	out := make([][]float64, len(x))
	parallel(len(x), func(lo, hi int) {
		for i := lo; i < hi; i++ {
			r := make([]float64, w2)
			for a, xa := range x[i] {
				if xa == 0 {
					continue
				}
				ma := m[a]
				for b := 0; b < w2; b++ {
					r[b] += xa * ma[b]
				}
			}
			out[i] = r
		}
	})
	return out
}

// jacobiEigen diagonalizes a symmetric matrix with cyclic Jacobi
// rotations; eigenvalues are returned descending with eigenvectors as
// the matching columns of v.
func jacobiEigen(g [][]float64) ([]float64, [][]float64) {
	n := len(g)
	a := make([][]float64, n)
	v := make([][]float64, n)
	for i := range a {
		a[i] = append([]float64(nil), g[i]...)
		v[i] = make([]float64, n)
		v[i][i] = 1
	}
	for sweep := 0; sweep < 60; sweep++ {
		var off, diag float64
		for i := 0; i < n; i++ {
			diag += a[i][i] * a[i][i]
			for j := i + 1; j < n; j++ {
				off += a[i][j] * a[i][j]
			}
		}
		if off <= 1e-24*diag || off == 0 {
			break
		}
		for p := 0; p < n; p++ {
			for q := p + 1; q < n; q++ {
				apq := a[p][q]
				if math.Abs(apq) < 1e-300 {
					continue
				}
				theta := (a[q][q] - a[p][p]) / (2 * apq)
				t := math.Copysign(1, theta) / (math.Abs(theta) + math.Sqrt(theta*theta+1))
				c := 1 / math.Sqrt(t*t+1)
				s := t * c
				for k := 0; k < n; k++ {
					akp, akq := a[k][p], a[k][q]
					a[k][p], a[k][q] = c*akp-s*akq, s*akp+c*akq
				}
				for k := 0; k < n; k++ {
					apk, aqk := a[p][k], a[q][k]
					a[p][k], a[q][k] = c*apk-s*aqk, s*apk+c*aqk
				}
				for k := 0; k < n; k++ {
					vkp, vkq := v[k][p], v[k][q]
					v[k][p], v[k][q] = c*vkp-s*vkq, s*vkp+c*vkq
				}
			}
		}
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool { return a[idx[i]][idx[i]] > a[idx[j]][idx[j]] })
	lam := make([]float64, n)
	vs := make([][]float64, n)
	for r := range vs {
		vs[r] = make([]float64, n)
	}
	for j, i := range idx {
		lam[j] = a[i][i]
		for r := 0; r < n; r++ {
			vs[r][j] = v[r][i]
		}
	}
	return lam, vs
}
