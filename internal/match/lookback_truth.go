package match

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// ---------- lookback: truth derivation and slug stripping ----------
//
// The lookback task: given task Q as it looked near its start, rank the
// tasks created before Q so that Q's related past tasks come first.
// "Related" is derived automatically from the user's data, one labelled
// source at a time (TruthSources). Sources 2-6 come from flow's
// structure (parent, project, slug family, playbook, tags). They define
// truth here and must never be used as a scoring feature in this eval,
// or the eval would only measure how well a scorer reads the labels.

// Truth sources, in report order.
const (
	SrcMention  = "mention"  // Q's text names C's slug, or C's text names Q's
	SrcParent   = "parent"   // parent/child link, or siblings under one parent
	SrcProject  = "project"  // same project
	SrcFamily   = "family"   // slug family: foo / foo-2, retro-<x>-*
	SrcPlaybook = "playbook" // playbook runs of one playbook
	SrcTag      = "tag"      // a shared, non-generic tag
)

// TruthSources lists every source in report order.
var TruthSources = []string{SrcMention, SrcParent, SrcProject, SrcFamily, SrcPlaybook, SrcTag}

// DefaultGenericTags are tags that mark a kind of task (an intake
// channel, a session role, a review format), not a topic, so sharing one
// says nothing about relatedness.
var DefaultGenericTags = []string{"slack", "driver", "record", "retro", "customer-ask", "question", "auto"}

// GenericTagShare: a tag on more than this share of all tasks is
// treated as generic too, whatever its name.
const GenericTagShare = 0.15

// FamilyPrefixes are slug prefixes whose families span the first two
// parts (retro-<x>-anything).
var FamilyPrefixes = map[string]bool{"retro": true}

var reFamilySuffix = regexp.MustCompile(`^(\d+|v\d+|p\d+|part\d*|followup|cont|continued|redo|again)$`)

// FamilyKey is the slug's family stem: "retro-<x>" for a FamilyPrefixes
// slug, else the slug with trailing numeric/variant parts dropped
// ("foo-2" -> "foo", "foo-v3" -> "foo"). A slug with no variant suffix
// is its own stem, so "foo" and "foo-2" share the key "foo". Stems
// shorter than 4 characters return "" (too generic to form a family).
func FamilyKey(slug string) string {
	parts := strings.Split(strings.ToLower(slug), "-")
	if len(parts) >= 3 && FamilyPrefixes[parts[0]] {
		return parts[0] + "-" + parts[1]
	}
	n := len(parts)
	for n > 1 && reFamilySuffix.MatchString(parts[n-1]) {
		n--
	}
	k := strings.Join(parts[:n], "-")
	if len(k) < 4 {
		return ""
	}
	return k
}

// ---------- slug matching ----------

// SlugMatcher finds and strips task-slug mentions in free text. Only
// multi-part slugs (containing a hyphen) are matched: a single-word slug
// ("gizmo") is an ordinary word, so its occurrences are neither mention
// truth nor stripped.
type SlugMatcher struct {
	byFirst map[string][][]string // first part -> slugs (as parts), longest first
}

// NewSlugMatcher indexes slugs (duplicates and single-part slugs are
// ignored).
func NewSlugMatcher(slugs []string) *SlugMatcher {
	sm := &SlugMatcher{byFirst: map[string][][]string{}}
	seen := map[string]bool{}
	for _, s := range slugs {
		s = strings.ToLower(s)
		if seen[s] || !strings.Contains(s, "-") {
			continue
		}
		seen[s] = true
		parts := strings.FieldsFunc(s, func(r rune) bool { return !isAlnum(byte(r)) })
		if len(parts) < 2 {
			continue
		}
		sm.byFirst[parts[0]] = append(sm.byFirst[parts[0]], parts)
	}
	for k, v := range sm.byFirst {
		sort.Slice(v, func(i, j int) bool {
			if len(v[i]) != len(v[j]) {
				return len(v[i]) > len(v[j])
			}
			return strings.Join(v[i], "-") < strings.Join(v[j], "-")
		})
		sm.byFirst[k] = v
	}
	return sm
}

func isAlnum(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') }

type span struct{ start, end int }

func alnumTokens(s string) []span {
	var out []span
	for i := 0; i < len(s); {
		if !isAlnum(s[i]) {
			i++
			continue
		}
		j := i
		for j < len(s) && isAlnum(s[j]) {
			j++
		}
		out = append(out, span{i, j})
		i = j
	}
	return out
}

// sepOK reports whether the gap between two slug parts is an accepted
// separator: exactly "-" in strict mode; one run of '-', '_' or spaces
// (at most 3 bytes) in loose mode (close variants: "foo bar", "foo_bar").
func sepOK(gap string, strict bool) bool {
	if strict {
		return gap == "-"
	}
	if gap == "" || len(gap) > 3 {
		return false
	}
	for i := 0; i < len(gap); i++ {
		if c := gap[i]; c != '-' && c != '_' && c != ' ' {
			return false
		}
	}
	return true
}

// scan calls hit(slug, start, end) for each non-overlapping, longest
// slug occurrence in lower-cased text.
func (sm *SlugMatcher) scan(low string, strict bool, hit func(slug string, start, end int)) {
	toks := alnumTokens(low)
	for i := 0; i < len(toks); i++ {
		cands := sm.byFirst[low[toks[i].start:toks[i].end]]
		if len(cands) == 0 {
			continue
		}
		if strict && toks[i].start > 0 && (low[toks[i].start-1] == '-' || low[toks[i].start-1] == '_') {
			continue // inside a longer hyphenated identifier
		}
		matched := 0
		for _, parts := range cands {
			if i+len(parts) > len(toks) {
				continue
			}
			ok := true
			for k := 1; k < len(parts) && ok; k++ {
				t := toks[i+k]
				ok = low[t.start:t.end] == parts[k] && sepOK(low[toks[i+k-1].end:t.start], strict)
			}
			if !ok {
				continue
			}
			end := toks[i+len(parts)-1].end
			if strict && end+1 < len(low) && (low[end] == '-' || low[end] == '_') && isAlnum(low[end+1]) {
				continue // "foo" inside "foo-2"
			}
			hit(strings.Join(parts, "-"), toks[i].start, end)
			matched = len(parts)
			break
		}
		if matched > 0 {
			i += matched - 1
		}
	}
}

// Find returns the slugs mentioned verbatim (hyphenated, on identifier
// boundaries) in text.
func (sm *SlugMatcher) Find(text string) map[string]bool {
	out := map[string]bool{}
	sm.scan(strings.ToLower(text), true, func(s string, _, _ int) { out[s] = true })
	return out
}

// Strip removes every slug mention and its close variants (parts joined
// by spaces or underscores, any case) from text, replacing each with a
// space. It returns the stripped text and how many spans it removed.
func (sm *SlugMatcher) Strip(text string) (string, int) {
	low := strings.ToLower(text)
	var spans []span
	sm.scan(low, false, func(_ string, s, e int) { spans = append(spans, span{s, e}) })
	if len(spans) == 0 {
		return text, 0
	}
	var b strings.Builder
	prev := 0
	for _, sp := range spans {
		b.WriteString(text[prev:sp.start])
		b.WriteByte(' ')
		prev = sp.end
	}
	b.WriteString(text[prev:])
	return b.String(), len(spans)
}

// ---------- truth ----------

// TruthSet is one query task's related past tasks, per source (sorted
// slugs, every one created strictly before the query task).
type TruthSet map[string][]string

// Union is every related task, any source (sorted).
func (t TruthSet) Union() []string {
	set := map[string]bool{}
	for _, xs := range t {
		for _, x := range xs {
			set[x] = true
		}
	}
	return sortedKeys(set)
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fullText is everything a task ever said about itself: name, brief,
// notes and prompts (truth may use the future; queries may not).
func fullText(td TaskData) string {
	var b strings.Builder
	b.WriteString(td.Name)
	b.WriteByte('\n')
	b.WriteString(td.Brief)
	for _, u := range td.Updates {
		b.WriteByte('\n')
		b.WriteString(u.Text)
	}
	for _, p := range td.Prompts {
		b.WriteByte('\n')
		b.WriteString(p)
	}
	return b.String()
}

// GenericTags returns the tags treated as generic for tasks: the named
// defaults plus any tag on more than GenericTagShare of tasks.
func GenericTags(tasks []TaskData, named []string) map[string]bool {
	g := map[string]bool{}
	for _, t := range named {
		g[strings.ToLower(t)] = true
	}
	n := map[string]int{}
	for _, td := range tasks {
		for _, t := range td.Tags {
			n[strings.ToLower(t)]++
		}
	}
	for t, c := range n {
		if float64(c) > GenericTagShare*float64(len(tasks)) {
			g[t] = true
		}
	}
	return g
}

// DeriveTruth labels, for every task Q, its related past tasks per
// source. Candidates are the tasks created strictly before Q. sm should
// index every known slug; generic lists tags that never count.
func DeriveTruth(tasks []TaskData, sm *SlugMatcher, generic map[string]bool) map[string]TruthSet {
	mentions := mentionGraph(tasks, sm)
	hubs := mentionHubs(mentions, len(tasks))
	out := map[string]TruthSet{}
	for _, q := range tasks {
		ts := TruthSet{}
		add := func(src, c string) {
			for _, x := range ts[src] {
				if x == c {
					return
				}
			}
			ts[src] = append(ts[src], c)
		}
		qTags := map[string]bool{}
		for _, t := range q.Tags {
			if t = strings.ToLower(t); !generic[t] {
				qTags[t] = true
			}
		}
		qFam := FamilyKey(q.Slug)
		for _, c := range tasks {
			if c.Slug == q.Slug || !(c.CreatedAt < q.CreatedAt) {
				continue
			}
			cl, ql := strings.ToLower(c.Slug), strings.ToLower(q.Slug)
			if (mentions[q.Slug][cl] || mentions[c.Slug][ql]) && !hubs[cl] && !hubs[ql] {
				add(SrcMention, c.Slug)
			}
			if q.Parent == c.Slug || c.Parent == q.Slug || (q.Parent != "" && q.Parent == c.Parent) {
				add(SrcParent, c.Slug)
			}
			if q.Project != "" && q.Project == c.Project {
				add(SrcProject, c.Slug)
			}
			if qFam != "" && qFam == FamilyKey(c.Slug) {
				add(SrcFamily, c.Slug)
			}
			if q.Kind == "playbook_run" && q.Playbook != "" && c.Kind == "playbook_run" && c.Playbook == q.Playbook {
				add(SrcPlaybook, c.Slug)
			}
			for _, t := range c.Tags {
				if qTags[strings.ToLower(t)] {
					add(SrcTag, c.Slug)
					break
				}
			}
		}
		for src := range ts {
			sort.Strings(ts[src])
		}
		if len(ts) > 0 {
			out[q.Slug] = ts
		}
	}
	return out
}

// MentionHubShare: a task whose mention degree (tasks naming it plus
// slugs it names) exceeds this share of all tasks is a hub — a watcher
// whose run log lists every task it spawned, or a driver named in
// boilerplate paths ("~/.flow/tasks/<hub>/updates/") — and its mention
// edges are not truth (structure sources still see it).
const MentionHubShare = 0.10

// MentionHubMin is the hub degree floor, so small corpora have no hubs.
const MentionHubMin = 20

// mentionGraph maps each task to the (lower-cased) slugs its full text
// names, itself excluded.
func mentionGraph(tasks []TaskData, sm *SlugMatcher) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(tasks))
	for _, td := range tasks {
		ms := sm.Find(fullText(td))
		delete(ms, strings.ToLower(td.Slug))
		out[td.Slug] = ms
	}
	return out
}

func mentionHubs(mentions map[string]map[string]bool, ntasks int) map[string]bool {
	// degree = distinct tasks naming s + distinct slugs s names
	in := map[string]int{}
	for t, ms := range mentions {
		for s := range ms {
			in[s]++
		}
		in[strings.ToLower(t)] += len(ms)
	}
	hubs := map[string]bool{}
	for s, n := range in {
		if float64(n) > math.Max(MentionHubShare*float64(ntasks), MentionHubMin) {
			hubs[s] = true
		}
	}
	return hubs
}

// MentionStat counts mention truth under one matching rule.
type MentionStat struct {
	Rule    string   `json:"rule"`
	Queries int      `json:"queries"`
	Pairs   int      `json:"pairs"`
	Hubs    []string `json:"hubs,omitempty"`
}

// MentionStats compares mention truth under progressively stricter
// rules: naive substring match of any slug (single-word included), the
// whole-token multi-part rule, and that plus the hub guard (the rule
// DeriveTruth uses).
func MentionStats(tasks []TaskData, sm *SlugMatcher) []MentionStat {
	low := make([]string, len(tasks))
	for i, t := range tasks {
		low[i] = strings.ToLower(fullText(t))
	}
	naive := MentionStat{Rule: "substring, any slug"}
	for i, q := range tasks {
		n := 0
		for j, c := range tasks {
			if c.CreatedAt < q.CreatedAt && c.Slug != q.Slug &&
				(strings.Contains(low[i], strings.ToLower(c.Slug)) || strings.Contains(low[j], strings.ToLower(q.Slug))) {
				n++
			}
		}
		if n > 0 {
			naive.Queries++
			naive.Pairs += n
		}
	}
	mentions := mentionGraph(tasks, sm)
	hubs := mentionHubs(mentions, len(tasks))
	count := func(rule string, useHubs bool) MentionStat {
		st := MentionStat{Rule: rule}
		for _, q := range tasks {
			n := 0
			ql := strings.ToLower(q.Slug)
			for _, c := range tasks {
				if !(c.CreatedAt < q.CreatedAt) || c.Slug == q.Slug {
					continue
				}
				cl := strings.ToLower(c.Slug)
				if (mentions[q.Slug][cl] || mentions[c.Slug][ql]) && !(useHubs && (hubs[cl] || hubs[ql])) {
					n++
				}
			}
			if n > 0 {
				st.Queries++
				st.Pairs += n
			}
		}
		if useHubs {
			st.Hubs = sortedKeys(hubs)
		}
		return st
	}
	return []MentionStat{naive, count("whole-token, multi-part slugs", false), count("+ hub guard (used)", true)}
}
