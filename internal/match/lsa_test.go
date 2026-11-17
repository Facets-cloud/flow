package match

import (
	"math"
	"math/rand/v2"
	"testing"
)

// TestRandomizedSVDMatchesExact checks the pure-Go randomized SVD on a
// small exactly-low-rank sparse matrix: singular values match the
// eigenvalues of AᵀA, and right vectors are orthonormal eigenvectors.
func TestRandomizedSVDMatchesExact(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 7))
	const n, v, r = 60, 40, 5
	// A = L · R with sparse-ish factors.
	l := make([][]float64, n)
	for i := range l {
		l[i] = make([]float64, r)
		for j := range l[i] {
			if rng.Float64() < 0.6 {
				l[i][j] = rng.NormFloat64()
			}
		}
	}
	rt := make([][]float64, r)
	for j := range rt {
		rt[j] = make([]float64, v)
		for k := range rt[j] {
			if rng.Float64() < 0.5 {
				rt[j][k] = rng.NormFloat64()
			}
		}
	}
	rows := make([]spvec, n)
	dense := make([][]float64, n)
	for i := 0; i < n; i++ {
		dense[i] = make([]float64, v)
		for k := 0; k < v; k++ {
			var s float64
			for j := 0; j < r; j++ {
				s += l[i][j] * rt[j][k]
			}
			dense[i][k] = s
			if s != 0 {
				rows[i].idx = append(rows[i].idx, int32(k))
				rows[i].val = append(rows[i].val, s)
			}
		}
	}
	sigma, vt := randomizedSVD(rows, v, LSAParams{K: r, Over: 5, Power: 2, Seed: 1})
	exact, _ := jacobiEigen(gram(dense, v))
	if len(sigma) != r {
		t.Fatalf("got %d singular values, want %d", len(sigma), r)
	}
	for d := 0; d < r; d++ {
		if want := math.Sqrt(exact[d]); math.Abs(sigma[d]-want) > 1e-8*want {
			t.Errorf("sigma[%d]=%g want %g", d, sigma[d], want)
		}
		for e := 0; e < r; e++ {
			want := 0.0
			if d == e {
				want = 1
			}
			if got := dot(vt[d], vt[e]); math.Abs(got-want) > 1e-8 {
				t.Errorf("v%d·v%d=%g want %g", d, e, got, want)
			}
		}
		// ‖A v‖ = σ.
		var ss float64
		for i := 0; i < n; i++ {
			x := dot(dense[i], vt[d])
			ss += x * x
		}
		if math.Abs(math.Sqrt(ss)-sigma[d]) > 1e-8*sigma[d] {
			t.Errorf("|A v%d| = %g, sigma %g", d, math.Sqrt(ss), sigma[d])
		}
	}
}

func lsaCorpus() []TaskData {
	return []TaskData{
		{Slug: "billing", Name: "billing invoices", CreatedAt: "2026-01-01",
			Brief:   "# What\nsend monthly invoices to customers\n\n# Why\nbilling for the quarter, invoice totals and payment reminders",
			Prompts: []string{"generate the invoice pdf for each customer", "billing totals look wrong for march", "send payment reminder emails with the invoice attached", "invoice numbering billing sequence"}},
		{Slug: "pg-repl", Name: "postgres logical replication", CreatedAt: "2026-01-02",
			Brief:   "# What\nset up logical replication from postgres primary\n\n# Why\nreplication slot lag and wal retention on the replica",
			Prompts: []string{"check replication slot lag", "wal retention keeps growing on the primary", "create the publication and subscription", "replica lag after vacuum"}},
		{Slug: "frontend", Name: "dashboard redesign", CreatedAt: "2026-01-03",
			Brief:   "# What\nredesign the react dashboard\n\n# Why\ncharts and layout are cluttered, css grid and dark mode",
			Prompts: []string{"move the chart legend into the header", "dark mode colors for the css grid", "react component for the layout sidebar", "dashboard chart tooltip"}},
	}
}

// TestLSACoOccurrenceBridgesSynonyms: "billing" never co-occurs with the
// query word "payment" in the frontend/db tasks, so a query using only
// co-occurring vocabulary of the billing task (no exact phrase match)
// should still rank billing first.
func TestLSACoOccurrenceBridgesSynonyms(t *testing.T) {
	var pts []LSAPoint
	for _, td := range lsaCorpus() {
		pts = append(pts, PointsOf(td)...)
	}
	sp := FitLSA(pts, LSAParams{K: 4, Alpha: 1, MinDF: 1, Power: 2, Seed: 1})
	for _, tc := range []struct{ q, want string }{
		{"payment reminder totals", "billing"},
		{"wal slot", "pg-repl"},
		{"legend tooltip sidebar", "frontend"},
	} {
		sc := sp.TaskScores(sp.Project(Counts(tc.q, 0)), 3, 0)
		best := argmax(sc, []string{"billing", "pg-repl", "frontend"})
		if best != tc.want {
			t.Errorf("%q: best %s (%v), want %s", tc.q, best, sc, tc.want)
		}
	}
	if sp.Project(Counts("zzzz qqqq", 0)) != nil {
		t.Error("all-OOV query should project to nil")
	}
}

// TestLSAFoldInMatchesRefitOnSameData: folding the training points back
// into a rebased space reproduces their coordinates exactly.
func TestLSAFoldInMatchesRefitOnSameData(t *testing.T) {
	var pts []LSAPoint
	for _, td := range lsaCorpus() {
		pts = append(pts, PointsOf(td)...)
	}
	sp := FitLSA(pts, LSAParams{K: 4, Alpha: 0.5, MinDF: 1, Seed: 1})
	inc := sp.Rebase()
	for _, p := range pts {
		inc.AddPoint(p)
	}
	for i := range sp.Coord {
		for d := range sp.Coord[i] {
			if math.Abs(float64(sp.Coord[i][d])-float64(inc.Coord[i][d])) > 1e-12 {
				t.Fatalf("point %d dim %d: %g vs %g", i, d, sp.Coord[i][d], inc.Coord[i][d])
			}
		}
	}
	if inc.NumPoints("billing") != sp.NumPoints("billing") {
		t.Fatal("point bookkeeping differs")
	}
}

func TestPointsOfSplitsBriefSections(t *testing.T) {
	td := lsaCorpus()[0]
	pts := PointsOf(td)
	kinds := map[int]int{}
	for _, p := range pts {
		kinds[p.Kind]++
	}
	if kinds[PtName] != 1 || kinds[PtBrief] != 2 || kinds[PtPrompt] != 4 {
		t.Fatalf("kinds = %v", kinds)
	}
}

// argmax returns the candidate in cands with the highest score in sc
// (lexicographic tie-break).
func argmax(sc map[string]float64, cands []string) string {
	best, bv := "", math.Inf(-1)
	for _, c := range cands {
		if v := sc[c]; v > bv || (v == bv && c < best) {
			best, bv = c, v
		}
	}
	return best
}
