package match

import (
	"math"
	"testing"
)

// list builds a ConfList from scores; rel marks relevant slugs by index.
func list(self float64, tokens int, scores []float64, rel ...int) *ConfList {
	l := &ConfList{Query: "q", Self: self, Tokens: tokens, strict: map[string]bool{}, lenient: map[string]bool{}}
	for i, s := range scores {
		slug := string(rune('a' + i))
		l.Items = append(l.Items, ConfItem{Slug: slug, Score: s})
	}
	for _, i := range rel {
		l.strict[l.Items[i].Slug] = true
		l.lenient[l.Items[i].Slug] = true
	}
	return l
}

func TestConfRuleShown(t *testing.T) {
	l := list(1.0, 100, []float64{0.50, 0.45, 0.40, 0.10, 0.09, 0.08})
	for _, c := range []struct {
		r    ConfRule
		want int
	}{
		{ConfRule{Kind: ConfFixed, Cap: 2}, 2},
		{ConfRule{Kind: ConfFixed, Cap: 10}, 6},
		{ConfRule{Kind: ConfRelative, T: 0.85, Cap: 5}, 2},            // 0.45 >= .425, 0.40 < .425
		{ConfRule{Kind: ConfRelative, T: 0.5, Cap: 5}, 3},             // stops at 0.10
		{ConfRule{Kind: ConfRelative, T: 0.5, Cap: 2}, 2},             // cap
		{ConfRule{Kind: ConfRelative, T: 0.3, Cap: 5, Floor: 0.6}, 0}, // floor empties it
		{ConfRule{Kind: ConfAbsolute, T: 0.42, Cap: 5}, 2},
		{ConfRule{Kind: ConfAbsolute, T: 0.9, Cap: 5}, 0},
		{ConfRule{Kind: ConfElbow, Cap: 5}, 3},              // biggest drop 0.40 -> 0.10
		{ConfRule{Kind: ConfElbow, Cap: 2}, 2},              // cap before the elbow
		{ConfRule{Kind: ConfElbow, Cap: 5, Floor: 0.48}, 1}, // floor inside the elbow
	} {
		if got := c.r.Shown(l); got != c.want {
			t.Errorf("%s: shown %d, want %d", c.r, got, c.want)
		}
	}
	// self-normalised: same raw scores, a stronger self score shows less.
	r := ConfRule{Kind: ConfSelf, T: 0.4, Cap: 5}
	if got := r.Shown(list(1.0, 100, []float64{0.5, 0.45, 0.3})); got != 2 {
		t.Errorf("self 1.0: shown %d, want 2", got)
	}
	if got := r.Shown(list(2.0, 100, []float64{0.5, 0.45, 0.3})); got != 0 {
		t.Errorf("self 2.0: shown %d, want 0", got)
	}
	// calibrated keeps the prefix with p >= T
	cl := list(1, 100, []float64{0.5, 0.4, 0.3})
	cl.p = []float64{0.9, 0.1, 0.8}
	if got := (ConfRule{Kind: ConfCalibrated, T: 0.5, Cap: 3}).Shown(cl); got != 1 {
		t.Errorf("calibrated: shown %d, want 1 (stops at the first miss)", got)
	}
}

func TestConfEvalMetrics(t *testing.T) {
	lists := []*ConfList{
		list(1, 50, []float64{0.6, 0.5, 0.1}, 0),  // shows 2, 1 right -> helps
		list(1, 50, []float64{0.6, 0.55, 0.5}),    // shows 3, none right
		list(1, 50, []float64{0.2, 0.19, 0.1}, 2), // shows 0; relevant at 3 -> missed
		list(1, 50, []float64{0.1, 0.05}),         // shows 0, nothing to find
	}
	s := ConfEval(ConfRule{Kind: ConfAbsolute, T: 0.45, Cap: 3}, lists, ConfStrict, "x")
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	if !near(s.Empty, 0.5) || !near(s.AvgShown, 5.0/4) || !near(s.AvgIfAny, 2.5) || !near(s.Precision, 1.0/5) ||
		!near(s.Helps, 0.25) || s.Reachable != 2 || !near(s.Missed, 0.5) || !near(s.HelpsReach, 0.5) {
		t.Fatalf("stats = %+v", s)
	}
}

func TestCalibratedLearnsScoreAndForwardChains(t *testing.T) {
	// relevant items are exactly the ones with score/self > 0.5
	var lists []*ConfList
	for q := 0; q < 60; q++ {
		var sc []float64
		var rel []int
		for i := 0; i < ConfTopN; i++ {
			v := 0.9 - 0.08*float64(i) - 0.01*float64(q%5)
			sc = append(sc, v)
			if v > 0.5 {
				rel = append(rel, i)
			}
		}
		l := list(1, 40+q, sc, rel...)
		l.Fold = q / 20 // folds 0, 1, 2
		lists = append(lists, l)
	}
	coefs := calibrate(lists, ConfStrict, 2)
	if len(coefs) != 2 || coefs[0].Items != 20*ConfTopN || coefs[1].Items != 40*ConfTopN {
		t.Fatalf("forward-chaining train sizes: %+v", coefs)
	}
	for _, l := range lists {
		if l.Fold == 0 {
			if l.p != nil {
				t.Fatal("train-only fold must not be scored")
			}
			continue
		}
		if !(l.p[0] > 0.7 && l.p[ConfTopN-1] < 0.3) {
			t.Fatalf("p not learned: %v", l.p)
		}
	}
	m := ShipFit(lists, ConfStrict)
	if p0, p9 := m.Prob(lists[0], 0), m.Prob(lists[0], ConfTopN-1); !(p0 > 0.7 && p9 < 0.3) {
		t.Fatalf("shipped fit p = %.2f / %.2f", p0, p9)
	}
}

func TestAnalyzeConfidencePicksPoints(t *testing.T) {
	var lists []*ConfList
	for q := 0; q < 100; q++ {
		sc := []float64{0.8, 0.7, 0.3, 0.2, 0.1}
		var rel []int
		switch q % 4 {
		case 0, 1:
			rel = []int{0} // strong top-1
		case 2:
			sc = []float64{0.2, 0.19, 0.18, 0.17, 0.16} // weak, unrelated
		}
		l := list(1, 50, sc, rel...)
		l.Query = string(rune('A'+q%26)) + string(rune('a'+q/26))
		l.Fold = q * 5 / 100
		lists = append(lists, l)
	}
	a := AnalyzeConfidence(lists, 4, "test", 1)
	if len(a.Points) == 0 || a.Recommended.Kind == "" {
		t.Fatalf("no operating points: %+v", a.Notes)
	}
	for _, p := range a.Points {
		if p.All.Precision < 0.5 {
			t.Errorf("%s below its precision goal: %+v", p.Name, p.All)
		}
	}
	if len(a.Examples) == 0 {
		t.Error("no examples")
	}
}

func TestRelatedConfidenceCutoff(t *testing.T) {
	tasks := synthLookback()
	q := tasks[len(tasks)-1]
	all := Related(tasks, q.Slug, 5, 0, true)
	if len(all.Top) != 5 {
		t.Fatalf("minP 0 must show the plain top k: %+v", all.Top)
	}
	for _, h := range all.Top {
		if h.P <= 0 || h.P >= 1 || (h.Label == LabelWeak) != (h.P < RelatedMinP) {
			t.Fatalf("bad p/label: %+v", h)
		}
	}
	none := Related(tasks, q.Slug, 5, 0.999999, true)
	if len(none.Top) != 0 {
		t.Fatalf("an unreachable cut-off must show nothing: %+v", none.Top)
	}
	def := Related(tasks, q.Slug, RelatedCap, RelatedMinP, true)
	if len(def.Top) > RelatedCap {
		t.Fatalf("cap ignored: %+v", def.Top)
	}
	for i, h := range def.Top {
		if h.P < RelatedMinP || h.Slug != all.Top[i].Slug {
			t.Fatalf("default cut-off must keep a confident prefix of the ranking: %+v vs %+v", def.Top, all.Top)
		}
	}
}
