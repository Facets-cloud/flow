//go:build recalleval

package match

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// WriteLookbackReport renders a LookbackReport as markdown tables (slugs
// only, never task text).
func WriteLookbackReport(w io.Writer, r LookbackReport) {
	fmt.Fprintf(w, "# flow match eval --mode lookback (seed %d)\n\n", r.Seed)
	fmt.Fprintf(w, "tasks: %d · generic tags: %s · queries skipped (no text after stripping): %d\n",
		r.Tasks, strings.Join(r.GenericTags, ","), r.SkippedNoText)
	fmt.Fprintf(w, "fold starts: %s · test queries: brief=%d brief+prompts=%d\n\n",
		strings.Join(r.FoldStarts, ", "), r.TestQueries[ModeBrief], r.TestQueries[ModePrompts])

	fmt.Fprintln(w, "## Truth sources")
	fmt.Fprintln(w, "| source | query tasks | as brief queries | as brief+prompts queries | pairs | mean/query | only this source |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|")
	for _, c := range r.TruthCounts {
		fmt.Fprintf(w, "| %s | %d | %d | %d | %d | %.1f | %d |\n", c.Source, c.Queries, c.QueriesBrief, c.QueriesPr, c.Pairs, c.MeanPerQ, c.OnlySource)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Mention truth by matching rule (query tasks / pairs):")
	for _, m := range r.MentionRules {
		h := ""
		if len(m.Hubs) > 0 {
			h = " — hubs dropped: " + strings.Join(m.Hubs, ", ")
		}
		fmt.Fprintf(w, "- %s: %d / %d%s\n", m.Rule, m.Queries, m.Pairs, h)
	}
	fmt.Fprintln(w)

	rowsWhere := func(variant, mode, src string) []LBRow {
		var out []LBRow
		for _, x := range r.Rows {
			if x.Variant == variant && x.Mode == mode && x.Source == src {
				out = append(out, x)
			}
		}
		return out
	}
	table := func(title string, rows []LBRow) {
		if len(rows) == 0 {
			return
		}
		fmt.Fprintf(w, "### %s (n=%d queries)\n", title, rows[0].Queries)
		fmt.Fprintln(w, "| scorer | R@5 | R@10 [95% CI] | R@20 | MRR [95% CI] | nDCG@10 | hit@5 | ΔMRR vs bm25 [CI] | ΔR@10 vs bm25 [CI] |")
		fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|")
		for _, x := range rows {
			d := "—"
			d10 := "—"
			if x.Scorer != LookbackRef {
				d = fmt.Sprintf("%+.3f [%+.3f,%+.3f]", x.DMRRRef, x.DMRRCI.Lo, x.DMRRCI.Hi)
				d10 = fmt.Sprintf("%+.3f [%+.3f,%+.3f]", x.DR10Ref, x.DR10CI.Lo, x.DR10CI.Hi)
			}
			fmt.Fprintf(w, "| %s | %.3f | %.3f [%.3f,%.3f] | %.3f | %.3f [%.3f,%.3f] | %.3f | %.3f | %s | %s |\n",
				x.Scorer, x.R5, x.R10, x.R10CI.Lo, x.R10CI.Hi, x.R20, x.MRR, x.MRRCI.Lo, x.MRRCI.Hi, x.NDCG10, x.Hit5, d, d10)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "## Overall")
	for _, v := range []string{VarFull, VarCutoff} {
		for _, m := range append(append([]string(nil), LookbackModes...), ModeBriefSubset) {
			table(fmt.Sprintf("%s · %s · all truth", m, v), rowsWhere(v, m, "all"))
		}
	}
	fmt.Fprintln(w, "## Same tasks, both modes (full-notes, non-project and mention truth)")
	for _, src := range []string{SrcNonProject, SrcMention} {
		for _, m := range []string{ModeBriefSubset, ModePrompts} {
			table(fmt.Sprintf("%s · %s", m, src), rowsWhere(VarFull, m, src))
		}
	}
	fmt.Fprintln(w, "## Per truth source (full-notes)")
	for _, m := range LookbackModes {
		for _, src := range append([]string{SrcNonProject}, TruthSources...) {
			table(fmt.Sprintf("%s · %s", m, src), rowsWhere(VarFull, m, src))
		}
	}
	// compact MRR grid: scorer x source, mode brief
	fmt.Fprintln(w, "## MRR / R@10 by source (brief, full-notes)")
	var names []string
	seen := map[string]bool{}
	for _, x := range rowsWhere(VarFull, ModeBrief, "all") {
		if !seen[x.Scorer] {
			seen[x.Scorer] = true
			names = append(names, x.Scorer)
		}
	}
	fmt.Fprintf(w, "| scorer | all | %s | %s |\n", SrcNonProject, strings.Join(TruthSources, " | "))
	fmt.Fprintf(w, "|---|---|---|%s\n", strings.Repeat("---|", len(TruthSources)))
	for _, n := range names {
		fmt.Fprintf(w, "| %s ", n)
		for _, src := range append([]string{"all", SrcNonProject}, TruthSources...) {
			v := "—"
			for _, x := range rowsWhere(VarFull, ModeBrief, src) {
				if x.Scorer == n {
					v = fmt.Sprintf("%.3f / %.2f", x.MRR, x.R10)
				}
			}
			fmt.Fprintf(w, "| %s ", v)
		}
		fmt.Fprintln(w, "|")
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "## Leakage checks")
	for _, l := range r.Leaks {
		fmt.Fprintf(w, "- %s: %.4g (%s)\n", l.Check, l.Value, l.Detail)
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "## Misses of the best scorer (%s, brief, full-notes): truth items outside top 20\n", r.Best)
	fmt.Fprintf(w, "(no overlap: shares no word with DF<=%.0f%%; vocabulary: shares none with DF<=%.0f%%; identifier: shares distinctive words but none of the candidate's distinctive name words; outranked: shares those too)\n",
		100*CommonDF, 100*DistinctDF)
	fmt.Fprintln(w, "| source | truth items | in top 20 | missed | no text overlap | vocabulary mismatch | identifier mismatch | outranked | empty candidate |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|")
	for _, m := range r.Misses {
		fmt.Fprintf(w, "| %s | %d | %d | %d | %d | %d | %d | %d | %d |\n", m.Source, m.Items, m.Top20, m.Missed, m.NoOverlap, m.Vocabulary, m.Identifier, m.Outranked, m.EmptyCand)
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "## Examples")
	for _, ex := range r.Examples {
		fmt.Fprintf(w, "- **%s** (%s, %s)\n", ex.Query, ex.Mode, ex.Scorer)
		for i, h := range ex.Top5 {
			mark := ""
			if len(h.Relevant) > 0 {
				mark = " ✓ " + strings.Join(h.Relevant, ",")
			}
			fmt.Fprintf(w, "  %d. %s (%.3f)%s\n", i+1, h.Slug, h.Score, mark)
		}
		var ts []string
		for s, srcs := range ex.Truth {
			rk := "—"
			if r, ok := ex.Ranks[s]; ok {
				rk = fmt.Sprint(r)
			}
			ts = append(ts, fmt.Sprintf("%s[%s]@%s", s, strings.Join(srcs, ","), rk))
		}
		sort.Strings(ts)
		fmt.Fprintf(w, "  truth: %s\n", strings.Join(ts, "; "))
	}
	fmt.Fprintln(w)
	if len(r.Weights) > 0 {
		fmt.Fprintln(w, "## Lookback-learned weights (standardized)")
		for _, x := range r.Weights {
			var ks []string
			for k := range x.Weights {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			var parts []string
			for _, k := range ks {
				parts = append(parts, fmt.Sprintf("%s=%.2f", k, x.Weights[k]))
			}
			fmt.Fprintf(w, "- %s fold %d (%d pairs): %s\n", x.Scorer, x.Fold, x.Train, strings.Join(parts, " "))
		}
		fmt.Fprintln(w)
	}
	if r.Confidence != nil {
		WriteConfidence(w, *r.Confidence)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(w, "- note: %s\n", n)
	}
}
