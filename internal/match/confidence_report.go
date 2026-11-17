package match

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

func pct(x float64) string { return fmt.Sprintf("%.0f%%", 100*x) }

// PlainSummary is a one-line, jargon-free description of an operating
// point, e.g. "shows 1-2 tasks, right 85% of the time, helps on 55% of
// new tasks, silent 40% of the time".
func PlainSummary(s ConfStats) string {
	return fmt.Sprintf("shows %.1f tasks when it shows any, right %s of the time, helps on %s of new tasks, silent %s of the time",
		s.AvgIfAny, pct(s.Precision), pct(s.Helps), pct(s.Empty))
}

// WriteConfidence renders the cut-off analysis.
func WriteConfidence(w io.Writer, a ConfAnalysis) {
	fmt.Fprintf(w, "## Confidence cut-off: show only confident matches (%s, mode %s, %d test queries)\n\n", a.Scorer, a.Mode, a.Queries)
	fmt.Fprintln(w, "Words used below: *right* = of the tasks shown, the share that are really related.")
	fmt.Fprintln(w, "*helps* = share of new tasks where at least one shown task is really related.")
	fmt.Fprintln(w, "*silent* = share of new tasks where nothing is shown. *missed* = of the new tasks that had a")
	fmt.Fprintln(w, "related past task somewhere in the top 10, the share where we showed nothing.")
	fmt.Fprintln(w, "Related = mention, parent, slug family or playbook (strict). The lenient column also counts same project / tag.")
	fmt.Fprintln(w)
	pts := func(title string, ps []ConfPoint) {
		fmt.Fprintf(w, "### %s\n", title)
		fmt.Fprintln(w, "| setting | rule | in plain words (all test tasks) | right: older / newer | helps: older / newer | missed | right (lenient) |")
		fmt.Fprintln(w, "|---|---|---|---|---|---|---|")
		for _, p := range ps {
			fmt.Fprintf(w, "| **%s** (%s) | %s | %s | %s / %s | %s / %s | %s | %s |\n", p.Name, p.Goal, p.Rule,
				PlainSummary(p.All), pct(p.Tune.Precision), pct(p.Held.Precision), pct(p.Tune.Helps), pct(p.Held.Helps),
				pct(p.All.Missed), pct(p.AllLen.Precision))
		}
		fmt.Fprintln(w)
	}
	pts(fmt.Sprintf("Operating points, any rule (older test folds %v / newer %v)", a.TuneFolds, a.HeldFolds), a.Points)
	pts("Operating points, simple rules only (no fitted model)", a.SimplePoints)
	fmt.Fprintf(w, "Recommended for `flow match related`: **%s**; items that also pass **%s** are labelled *strong*, the rest *possible*.\n\n", a.Recommended, a.Strong)

	if len(a.Examples) > 0 {
		fmt.Fprintln(w, "### What the user would see (recommended rule)")
		for _, ex := range a.Examples {
			if len(ex.Shown) == 0 {
				fmt.Fprintf(w, "- **%s**: nothing shown", ex.Query)
			} else {
				var parts []string
				for _, s := range ex.Shown {
					m := "✗"
					if s.Relevant {
						m = "✓"
					}
					parts = append(parts, fmt.Sprintf("%s (%s) %s", s.Slug, s.Label, m))
				}
				fmt.Fprintf(w, "- **%s**: %s", ex.Query, strings.Join(parts, " · "))
			}
			var ts []string
			for s, r := range ex.Truth {
				ts = append(ts, fmt.Sprintf("%s@%d", s, r))
			}
			sort.Strings(ts)
			if len(ts) == 0 {
				ts = []string{"none in top 10"}
			}
			fmt.Fprintf(w, " — related in top 10: %s\n", strings.Join(ts, ", "))
		}
		fmt.Fprintln(w)
	}

	// best of each rule kind, all test queries, at the balanced bar
	for _, truth := range []string{ConfStrict, ConfLenient} {
		fmt.Fprintf(w, "### Best rule of each kind that is right ≥%.0f%% of the time (%s truth, all test queries; picked on the same queries, so optimistic)\n", 100*ConfBalancedPrec, truth)
		fmt.Fprintln(w, "| kind | rule | silent | avg shown | right | helps | missed |")
		fmt.Fprintln(w, "|---|---|---|---|---|---|---|")
		best := map[string]ConfStats{}
		var kinds []string
		for _, s := range a.Stats {
			if s.Truth != truth || s.Precision < ConfBalancedPrec {
				continue
			}
			b, ok := best[s.Rule.Kind]
			if !ok {
				kinds = append(kinds, s.Rule.Kind)
			}
			if !ok || s.Helps > b.Helps+1e-9 || (s.Helps >= b.Helps-1e-9 && s.AvgShown < b.AvgShown-1e-9) {
				best[s.Rule.Kind] = s
			}
		}
		for _, k := range kinds {
			s := best[k]
			fmt.Fprintf(w, "| %s | %s | %s | %.2f | %s | %s | %s |\n", k, s.Rule, pct(s.Empty), s.AvgShown, pct(s.Precision), pct(s.Helps), pct(s.Missed))
		}
		fmt.Fprintln(w)
	}

	for _, truth := range []string{ConfStrict, ConfLenient} {
		fmt.Fprintf(w, "<details><summary>Every rule (%s truth, all test queries)</summary>\n\n", truth)
		fmt.Fprintln(w, "| rule | silent | avg shown | shown if any | right | helps | missed |")
		fmt.Fprintln(w, "|---|---|---|---|---|---|---|")
		for _, s := range a.Stats {
			if s.Truth == truth {
				fmt.Fprintf(w, "| %s | %s | %.2f | %.2f | %s | %s | %s |\n", s.Rule, pct(s.Empty), s.AvgShown, s.AvgIfAny, pct(s.Precision), pct(s.Helps), pct(s.Missed))
			}
		}
		fmt.Fprintln(w, "\n</details>")
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "Shipped calibrated fit (all queries, strict): mean=%v std=%v w=%v b=%v\n\n", fmtF(a.Ship.Mean), fmtF(a.Ship.Std), fmtF(a.Ship.W), fmtF([]float64{a.Ship.B}))
	if len(a.Coefs) > 0 {
		fmt.Fprintln(w, "Calibrated rule fits (standardised weights):")
		for _, c := range a.Coefs {
			var parts []string
			for _, n := range ConfFeatureNames {
				parts = append(parts, fmt.Sprintf("%s=%.2f", n, c.W[n]))
			}
			fmt.Fprintf(w, "- %s fold %d (%d items, base rate %.2f): %s\n", c.Truth, c.Fold, c.Items, c.Base, strings.Join(parts, " "))
		}
		fmt.Fprintln(w)
	}
	for _, n := range a.Notes {
		fmt.Fprintf(w, "- note: %s\n", n)
	}
	fmt.Fprintln(w)
}

func fmtF(xs []float64) string {
	var p []string
	for _, x := range xs {
		p = append(p, fmt.Sprintf("%.6g", x))
	}
	return "{" + strings.Join(p, ", ") + "}"
}
