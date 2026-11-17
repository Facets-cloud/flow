package match

import "sort"

// Scorer is one way of ranking a query's candidate tasks. The recall eval
// (RunLookback) runs every lineup Scorer over identical folds.
//
// Fit sees only balanced training pairs from strictly older tasks (the
// lexical baselines use it to pick a threshold; the shipped ranker is
// parameter-free). Score must be monotone in "belongs"; it is used to rank
// candidate tasks for top-k retrieval. Belongs is the thresholded verdict.
type Scorer interface {
	Name() string
	Fit(train []Pair)
	Score(p Pair) float64
	Belongs(p Pair) bool
}

// Preparer is an optional Scorer extension for methods that build their
// own index: Prepare sees a pair set's indexed corpus and queries before
// any Fit (training set) or Score (test set) call on that set's pairs.
type Preparer interface {
	Prepare(ps *PairSet)
}

func prepare(scorers []Scorer, ps *PairSet) {
	for _, s := range scorers {
		if p, ok := s.(Preparer); ok {
			p.Prepare(ps)
		}
	}
}

// ThresholdScorer thresholds one raw signal at its training-F1-optimal
// cut.
type ThresholdScorer struct {
	Label string
	F     func(Pair) float64
	Thr   float64
}

func (s *ThresholdScorer) Name() string { return s.Label }

func (s *ThresholdScorer) Fit(train []Pair) {
	scores := make([]float64, len(train))
	labels := make([]bool, len(train))
	for i, p := range train {
		scores[i], labels[i] = s.F(p), p.Pos
	}
	s.Thr = f1Threshold(scores, labels)
}

func (s *ThresholdScorer) Score(p Pair) float64 { return s.F(p) }
func (s *ThresholdScorer) Belongs(p Pair) bool  { return s.F(p) >= s.Thr }

// f1Threshold picks the class-balanced F1-maximizing cut on scores.
func f1Threshold(scores []float64, labels []bool) float64 {
	type sl struct {
		s float64
		y bool
	}
	xs := make([]sl, len(scores))
	var npos int
	for i := range scores {
		xs[i] = sl{scores[i], labels[i]}
		if labels[i] {
			npos++
		}
	}
	if npos == 0 || npos == len(xs) {
		return 0.5
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i].s > xs[j].s })
	wpos := float64(len(xs)) / (2 * float64(npos))
	wneg := float64(len(xs)) / (2 * float64(len(xs)-npos))
	tot := wpos * float64(npos)
	var tp, fp float64
	best, bestT := -1.0, xs[0].s
	for i, x := range xs {
		if x.y {
			tp += wpos
		} else {
			fp += wneg
		}
		if i+1 < len(xs) && xs[i+1].s == x.s {
			continue
		}
		pr, rc := tp/(tp+fp), tp/tot
		if pr+rc > 0 {
			if f := 2 * pr * rc / (pr + rc); f > best {
				best, bestT = f, x.s
				if i+1 < len(xs) {
					bestT = (x.s + xs[i+1].s) / 2
				}
			}
		}
	}
	return bestT
}
