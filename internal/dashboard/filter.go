package dashboard

import (
	"slices"
	"sort"
	"strings"
)

// filter captures the active narrowing applied on top of the raw
// snapshot. All fields combine with AND. Empty fields are "no filter
// on this axis".
type filter struct {
	query    string   // free-text substring; matches slug, project, name, tags, assignee
	priority string   // "" | "high" | "medium" | "low"
	assignee string   // "" | exact assignee name
	tag      string   // "" | exact tag value (single-tag facet for now)
}

func (f filter) empty() bool {
	return f.query == "" && f.priority == "" && f.assignee == "" && f.tag == ""
}

// matches reports whether row r passes the filter.
func (f filter) matches(r TaskRow) bool {
	if f.priority != "" && r.Task.Priority != f.priority {
		return false
	}
	if f.assignee != "" {
		if !r.Task.Assignee.Valid || r.Task.Assignee.String != f.assignee {
			return false
		}
	}
	if f.tag != "" && !slices.Contains(r.Tags, f.tag) {
		return false
	}
	if f.query != "" {
		// Short identifiers go through fuzzy match (fzf-style subsequence)
		// so abbreviations like `btmig` find `budget-migration`.
		fuzzyHay := []string{r.Task.Slug, r.ProjectSlug}
		if r.Task.Assignee.Valid {
			fuzzyHay = append(fuzzyHay, r.Task.Assignee.String)
		}
		fuzzyHay = append(fuzzyHay, r.Tags...)

		ok := false
		for _, h := range fuzzyHay {
			if fuzzyMatch(f.query, h) {
				ok = true
				break
			}
		}
		// The Task.Name is a free-form sentence and would false-match
		// almost anything under fuzzy semantics (e.g. "omendra" hits
		// any long string with o-m-e-n-d-r-a in order). Require a real
		// substring hit on Name.
		if !ok && r.Task.Name != "" {
			if strings.Contains(strings.ToLower(r.Task.Name), strings.ToLower(f.query)) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// fuzzyMatch reports whether every character of needle appears in
// haystack in order (not necessarily contiguous). Case-insensitive.
// This is the classic subsequence-match used by VS Code / fzf for
// the "type a few letters of the slug" interaction — `btmig` will
// match `budget-migration` or `bot-imager-merge`.
func fuzzyMatch(needle, haystack string) bool {
	return fuzzyMatchPositions(needle, haystack) != nil
}

// fuzzyMatchPositions returns the byte indices in haystack where each
// character of needle matched, or nil when there's no full match.
// Used by the row renderer to highlight the matched characters.
func fuzzyMatchPositions(needle, haystack string) []int {
	if needle == "" {
		return nil
	}
	n := strings.ToLower(needle)
	h := strings.ToLower(haystack)
	positions := make([]int, 0, len(n))
	i := 0
	for j := 0; j < len(h) && i < len(n); j++ {
		if n[i] == h[j] {
			positions = append(positions, j)
			i++
		}
	}
	if i < len(n) {
		return nil
	}
	return positions
}

// filterRows returns the subset of rows that pass f. The Counts on
// the parent snapshot are NOT updated by this function — the header
// pill always shows raw counts so the user keeps sight of the actual
// workload while narrowing the view.
func filterRows(rows []TaskRow, f filter) []TaskRow {
	if f.empty() {
		return rows
	}
	out := make([]TaskRow, 0, len(rows))
	for _, r := range rows {
		if f.matches(r) {
			out = append(out, r)
		}
	}
	return out
}

// filterSnapshot returns a shallow copy of snap with each row section
// filtered. Activity events are passed through unmodified — when the
// user searches "budget", the activity log still shows everything
// recent so they can spot context they might've forgotten.
func filterSnapshot(snap *Snapshot, f filter) *Snapshot {
	if snap == nil || f.empty() {
		return snap
	}
	out := *snap
	out.Working = filterRows(snap.Working, f)
	out.Awaiting = filterRows(snap.Awaiting, f)
	out.Stale = filterRows(snap.Stale, f)
	out.Backlog = filterRows(snap.Backlog, f)
	return &out
}

// uniqueAssignees scans every task section of snap and returns a
// sorted, deduped list of assignees present in the data. Used by the
// `a` hotkey to cycle through real values rather than free-typing.
func uniqueAssignees(snap *Snapshot) []string {
	if snap == nil {
		return nil
	}
	set := map[string]struct{}{}
	for _, rows := range [][]TaskRow{snap.Working, snap.Awaiting, snap.Stale, snap.Backlog} {
		for _, r := range rows {
			if r.Task.Assignee.Valid && r.Task.Assignee.String != "" {
				set[r.Task.Assignee.String] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// uniqueTags returns a sorted, deduped list of every tag carried by
// any task in snap. Used by the `t` hotkey.
func uniqueTags(snap *Snapshot) []string {
	if snap == nil {
		return nil
	}
	set := map[string]struct{}{}
	for _, rows := range [][]TaskRow{snap.Working, snap.Awaiting, snap.Stale, snap.Backlog} {
		for _, r := range rows {
			for _, t := range r.Tags {
				set[t] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// cycleString advances cur within the values list, wrapping around
// from the last value back through "" (the no-filter state). Used by
// the priority/assignee/tag hotkeys.
func cycleString(cur string, values []string) string {
	if len(values) == 0 {
		return ""
	}
	if cur == "" {
		return values[0]
	}
	for i, v := range values {
		if v == cur {
			if i+1 < len(values) {
				return values[i+1]
			}
			return "" // wrap back to "no filter"
		}
	}
	return values[0]
}
