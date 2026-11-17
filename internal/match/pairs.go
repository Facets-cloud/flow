package match

import "sort"

// This file holds the query/candidate scoring primitives the recall pipeline
// and its eval share: a QueryRec (one query with its per-candidate signals), a
// Pair (one query↔task judgment a Scorer reads), and the PairSet that binds
// them to an index. The lexical-set similarities (Jaccard / overlap) used by
// the baseline scorers live here too.

// DefaultSeed fixes the tie-break/sampling so runs are reproducible.
const DefaultSeed = 1

// EvalFolds is the number of forward-chaining test windows the recall eval
// and the calibration refit split queries into.
const EvalFolds = 4

// informative reports whether a prompt has enough tokens to be worth indexing
// as query text (filters one-word acks and boilerplate).
func informative(p string) bool { return len(Tokenize(p)) >= MinQueryTokens }

// informativePrompts returns a task's prompts that clear the informative bar,
// in order. The slim cache and the query builders use the leading few as a
// task's own query text.
func informativePrompts(td TaskData) []string {
	var out []string
	for _, p := range td.Prompts {
		if informative(p) {
			out = append(out, p)
		}
	}
	return out
}

// QueryRec is one query (a task's own text, a free-text string, or a prompt)
// with its per-candidate signals, precomputed so a Scorer can read them in
// O(1) and pair() can rank in O(log n).
type QueryRec struct {
	Owner     string
	Q         Query
	text      string   // the (stripped) query text, for scorers that re-extract terms (idcard)
	Cands     []string // candidate task slugs (the owner may be included)
	sig       map[string]Signals
	jac, ovl  map[string]float64
	OwnerRank []int     // per scorer: 1-based rank of the true task among Cands
	primary   []float64 // candidate retrieval scores, descending
	top       [4]top2   // Sim, KNN, Cov, BM25
}

// top2 tracks the best two candidates of one signal, so "best other"
// for any target is O(1).
type top2 struct {
	slug   string
	v1, v2 float64
}

func (t *top2) add(slug string, v float64) {
	switch {
	case t.slug == "" || v > t.v1:
		t.v2 = t.v1
		t.slug, t.v1 = slug, v
	case v > t.v2:
		t.v2 = v
	}
}

func (t top2) other(slug string) float64 {
	if slug == t.slug {
		return t.v2
	}
	return t.v1
}

// Pair is one (query, target task) judgment a Scorer reads.
type Pair struct {
	Query   int // index into PairSet.Queries
	Target  string
	Pos     bool // query's owner == target
	Drift   bool // query comes from the task the user moved to after target
	A       Assessment
	Jaccard float64 // naive baselines: unigram sets of query vs task
	Overlap float64
	Score   []float64 // per scorer
	Pred    []bool
}

// PairSet is a set of queries plus the index they are scored on.
type PairSet struct {
	Model   *Model
	Queries []QueryRec
	Pairs   []Pair
	Held    []string
	// Corpus is exactly what Model indexes, for scorers that build their own
	// index (see Preparer).
	Corpus []TaskData
}

// unigramSet drops bigrams, leaving the unigram terms the set-similarity
// baselines compare.
func unigramSet(terms map[string]bool) map[string]bool {
	out := make(map[string]bool, len(terms))
	for t := range terms {
		if !IsBigram(t) {
			out[t] = true
		}
	}
	return out
}

// pair builds the (query, t) judgment, competing against the query's
// candidates, in O(log n) via the precomputed top-2s and sorted scores.
func (qr *QueryRec) pair(qi int, t string, drift bool) Pair {
	sg := qr.sig[t]
	own := sg.Primary()
	// rank = 1 + #candidates scoring strictly higher (t never beats itself).
	higher := sort.Search(len(qr.primary), func(i int) bool { return qr.primary[i] <= own })
	a := Assessment{
		Target: t, Tokens: qr.Q.Tokens, Rank: 1 + higher,
		Sim: sg.Sim, KNN: sg.KNN, Cov: sg.Cov, BM25: sg.BM25,
		BestOther: qr.top[0].other(t), KNNOther: qr.top[1].other(t),
		CovOther: qr.top[2].other(t), BM25Other: qr.top[3].other(t),
	}
	return Pair{Query: qi, Target: t, Pos: qr.Owner == t, Drift: drift, A: a, Jaccard: qr.jac[t], Overlap: qr.ovl[t]}
}

// setSims returns the Jaccard and overlap coefficients of two term sets.
func setSims(q, d map[string]bool) (jaccard, overlap float64) {
	if len(q) == 0 {
		return 0, 0
	}
	inter := 0
	for t := range q {
		if d[t] {
			inter++
		}
	}
	if union := len(q) + len(d) - inter; union > 0 {
		jaccard = float64(inter) / float64(union)
	}
	return jaccard, float64(inter) / float64(len(q))
}
