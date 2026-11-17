package match

import "strings"

// RelatedScorer names the scorer `flow match related` ranks with: the
// best on the lookback eval (pooled BM25 + 0.25 x LSA latent kNN; the
// only scorer whose paired MRR gain over BM25 has a CI above zero — see
// the kept/dropped ledger in docs/recall.md).
const RelatedScorer = "bm25+0.25lsa"

// ShippedRelatedScorer is what `flow match related` actually ranks with:
// bm25+0.25lsa fused with the identity-card score. On the layers eval the
// card lifts mention recall@10 (+.017, CI above 0) over bm25+0.25lsa with
// MRR unchanged, so it is the kept layer-2 ranker.
const ShippedRelatedScorer = "bm25+0.25lsa+card"

// shippedRelatedScorer builds the kept layer-2 ranker. index/space are the
// disk-cached card index and LSA space (nil = build/fit on the fly).
func shippedRelatedScorer(index *CardIndex, space *LSASpace) *CardScorer {
	return &CardScorer{Label: ShippedRelatedScorer, K: RelatedCardK, Norm: CardNormQuery,
		Lambda: 1, LSALambda: 0.25, Index: index, Space: space}
}

// Confidence cut-off `flow recall` applies by default: show item i while
// p(relevant) >= RelatedMinP, at most RelatedCap items, nothing when
// nothing clears it. p comes from RelatedConf, a logistic over (score/self,
// log rank, gap to next/self, log query tokens, query coverage) fit on the
// lookback eval (seed 1, strict truth: mention / parent / family /
// playbook). Query coverage — the IDF-weighted share of the query a
// candidate matches — is what keeps a top hit that only shares one strong
// word from being called strong on rank alone.
const (
	RelatedCap     = 2
	RelatedMinP    = 0.20
	RelatedStrongP = 0.60 // right ~81% of the time on the eval
)

// RelatedConf is the shipped calibrated fit for the shipped ranker
// (bm25+0.25lsa+card, coverage soft-AND), from `flow recall eval --seed 1`
// (the report's "Shipped calibrated fit", strict truth). The feature order
// is confFeatures: score/self, log rank, gap-to-next/self, log query
// tokens, query coverage. Refit and paste again if the ranker, its score
// scale or the feature set changes.
var RelatedConf = ConfModel{
	Mean: []float64{0.224876, 1.508591, 0.021388, 5.581022, 0.18477},
	Std:  []float64{0.184912, 0.695965, 0.058195, 1.003829, 0.184321},
	W:    []float64{0.832806, -0.557134, 0.080007, 0.95328, 0.538571},
	B:    -1.954811,
}

// Confidence labels.
const (
	LabelStrong   = "strong"
	LabelPossible = "possible"
	LabelWeak     = "weak" // below RelatedMinP: only shown when --min is lowered
)

// RelatedHit is one related past task — a layer-4 one-liner: slug,
// status, name and a one-line summary, plus the layer-2 score and
// layer-3 confidence.
type RelatedHit struct {
	Slug    string  `json:"slug"`
	Score   float64 `json:"score"`
	P       float64 `json:"p"`     // calibrated chance it is really related
	Label   string  `json:"label"` // strong / possible / weak
	Status  string  `json:"status"`
	Name    string  `json:"name"`
	Summary string  `json:"summary"` // one line from the task's brief
}

// RelatedResult is the lookback nudge for one task.
type RelatedResult struct {
	Task       string       `json:"task"`
	Scorer     string       `json:"scorer"`
	Mode       string       `json:"mode"`
	Candidates int          `json:"candidates"`
	MinP       float64      `json:"min_p"`
	Self       float64      `json:"self_score"`
	Project    string       `json:"project,omitempty"`
	Abstain    bool         `json:"abstain,omitempty"` // prompt too short / content-free: no guess made
	Confident  []RelatedHit `json:"confident"`         // layer-3: the subset at p >= RelatedStrongP
	Top        []RelatedHit `json:"top"`
}

// RelatedOptions configures the layered related pipeline.
type RelatedOptions struct {
	K        int     // layer 4: how many one-liners to emit
	MinP     float64 // layer 3: confidence cut (0 = plain top k)
	PastOnly bool    // layer 0: only tasks created before the query
	// Layer 0 hard filters:
	ExcludeChildren bool   // drop the query's own children and own playbook runs
	Project         string // isolate to a project slug or a slug prefix ("" = no isolation)
	// ExcludeBound, when set, drops the session's bound task and its own
	// children / playbook runs from the candidates in every mode — a bound
	// session never recalls the task it is sitting in (the CLI clears this
	// on --include-self). "" = no bound exclusion (unbound session).
	ExcludeBound string
	// Space, when set, is a pre-fit LSA space (disk cache) used verbatim so
	// the call skips the SVD. Its Pts must cover tasks; Reindex it first.
	Space *LSASpace
	// Model, when set, is a pre-built index (disk cache) over the same
	// tasks, so the call skips re-tokenizing every task.
	Model *Model
	// Cards, when set, is the pre-built identity-card index (disk cache) the
	// shipped ranker fuses in, so the call skips rebuilding the cards.
	Cards *CardIndex
	// Conf, when set, overrides the layer-3 confidence model (default
	// RelatedConf). `flow recall train --calibrate` fits a per-user model and
	// passes it here.
	Conf *ConfModel
}

// BuildRelatedIndex builds the in-memory term index the related pipeline
// ranks over. Cached to disk and restored via RelatedOptions.Model. The
// per-prompt kNN vectors are dropped: bm25+0.25lsa ranks by pooled BM25
// plus the latent score, never the kNN signal, so they are dead weight in
// the cache (and the slowest part to decode).
func BuildRelatedIndex(tasks []TaskData) *Model {
	m := NewModel(DefaultWeights)
	for _, t := range tasks {
		m.Upsert(t)
	}
	m.DropKNN()
	return m
}

// DropKNN frees the per-prompt kNN vectors from every task entry. Safe
// for scorers that do not use the KNN signal (bm25+0.25lsa); m.KNN then
// returns 0 for every task.
func (m *Model) DropKNN() {
	for _, e := range m.Tasks {
		e.PromptTF = nil
	}
	m.knnVecs = nil
}

// SlimForCache returns a copy of td carrying only the fields the related
// pipeline reads from the cached task list (layer 0 structure, layer 4
// name + brief). The heavy per-task prompt and note text is dropped; the
// query task is re-hydrated on demand for its own query text.
func SlimForCache(td TaskData) TaskData {
	s := TaskData{
		Slug: td.Slug, Name: td.Name, Status: td.Status, Archived: td.Archived,
		CreatedAt: td.CreatedAt, DoneAt: td.DoneAt, Brief: td.Brief,
		Project: td.Project, Parent: td.Parent, Kind: td.Kind, Playbook: td.Playbook,
		Tags: td.Tags, Fingerprint: td.Fingerprint,
	}
	// Keep only the first few informative prompts: that is all queryText
	// (name + brief + first 3 prompts) needs when this task is the query,
	// and those early prompts are stable as the transcript grows — so a
	// live `related` call never re-hydrates the full transcript. The task's
	// full prompt history is already in the cached term index.
	if inf := informativePrompts(td); len(inf) > 0 {
		if len(inf) > SlimPrompts {
			inf = inf[:SlimPrompts]
		}
		s.Prompts = append([]string(nil), inf...)
	}
	return s
}

// SlimPrompts is how many leading informative prompts the slim cache keeps
// per task (queryText uses the first 3).
const SlimPrompts = 4

// ReplaceTasks folds an incremental corpus change into an already-fit LSA
// space WITHOUT refitting the basis: it drops every point of the tasks in
// `drop` and folds `newPoints` (the changed/added tasks' points) onto the
// existing basis V. The basis stays as last fit — good enough while only a
// few docs change between calls; callers refit fully when too much drifts.
func (s *LSASpace) ReplaceTasks(drop map[string]bool, newPoints []LSAPoint) {
	if len(drop) > 0 {
		keptPts := s.Pts[:0:0]
		keptCoord := s.Coord[:0:0]
		for i, pt := range s.Pts {
			if !drop[pt.Slug] {
				keptPts = append(keptPts, pt)
				keptCoord = append(keptCoord, s.Coord[i])
			}
		}
		s.Pts, s.Coord = keptPts, keptCoord
	}
	s.Reindex()
	for _, pt := range newPoints {
		s.AddPoint(pt)
	}
}

// FitRelatedSpace fits the LSA latent space the related pipeline ranks with
// (bm25+0.25lsa), over every task's points. This is the expensive part of a
// live recall call (an SVD), so it is cached to disk and restored via
// RelatedOptions.Space.
func FitRelatedSpace(tasks []TaskData) *LSASpace {
	var pts []LSAPoint
	for _, td := range tasks {
		pts = append(pts, PointsOf(td)...)
	}
	return FitLSA(pts, DefaultLSAParams.withDefaults())
}

// Related ranks the tasks related to slug, best first. The query is the
// task's name + brief, plus its first 3 informative prompts when it has
// any (mode brief+prompts). Slug mentions are kept: unlike in the eval,
// a mention is real, legitimate evidence at run time. With pastOnly,
// candidates are the tasks created before slug; otherwise every other
// task. Items are shown best first while their calibrated p >= minP, at
// most k of them (minP <= 0 shows the plain top k).
func Related(tasks []TaskData, slug string, k int, minP float64, pastOnly bool) RelatedResult {
	return RelatedWith(tasks, slug, RelatedOptions{K: k, MinP: minP, PastOnly: pastOnly})
}

// RelatedWith runs the full layered pipeline: layer 0 (hard filters) →
// layer 1/2 (rank every surviving candidate with bm25+0.25lsa) → layer 3
// (confidence cut at opt.MinP) → layer 4 (one-liners). The confident
// subset (p >= RelatedStrongP) is surfaced separately.
func RelatedWith(tasks []TaskData, slug string, opt RelatedOptions) RelatedResult {
	k, minP := opt.K, opt.MinP
	res := RelatedResult{Task: slug, Scorer: ShippedRelatedScorer, MinP: minP, Project: opt.Project}
	var q *TaskData
	for i := range tasks {
		if tasks[i].Slug == slug {
			q = &tasks[i]
		}
	}
	if q == nil {
		return res
	}
	m := opt.Model
	if m == nil {
		m = NewModel(DefaultWeights)
		for _, t := range tasks {
			m.Upsert(t)
		}
	}
	byslug := make(map[string]TaskData, len(tasks))
	for _, t := range tasks {
		byslug[t.Slug] = t
	}
	// ---- layer 0: hard filters ----
	cust := projectMatcher(tasks, opt.Project)
	var cands []string
	for _, t := range tasks {
		if t.Slug == slug {
			continue
		}
		if opt.PastOnly && !(t.CreatedAt < q.CreatedAt) {
			continue
		}
		if opt.ExcludeChildren && isOwnDescendant(*q, t) {
			continue
		}
		if excludesBound(t, opt.ExcludeBound) {
			continue
		}
		if cust != nil && !cust(t) {
			continue
		}
		cands = append(cands, t.Slug)
	}
	res.Candidates = len(cands)
	mode := ModeBrief
	text, _ := queryText(*q, ModeBrief, 3)
	if t, ok := queryText(*q, ModePrompts, 3); ok {
		text, mode = t, ModePrompts
	}
	res.Mode = mode
	res.Scorer = ShippedRelatedScorer
	ps := &PairSet{Model: m, Corpus: tasks}
	qr := newQueryRec(m, &termCache{m: m, t: map[string]map[string]bool{}}, slug, lbQuery(m, text), cands)
	qr.text = text // the card scorer re-extracts entity terms from the query
	ps.Queries = []QueryRec{qr}
	if opt.Space != nil {
		opt.Space.Reindex()
	}
	cards := opt.Cards
	if cards == nil {
		cards = BuildRelatedCards(tasks)
	}
	s := shippedRelatedScorer(cards, opt.Space)
	bindTo(s, ps)
	// Drop no-overlap candidates (never pad zeros), then cut by confidence.
	// Features as in the eval (which stripped slugs from the query; here a
	// mention is kept, so the token count can differ by a few).
	hits := filterOverlap(m, qr.Q, s, rankAll(s, ps, 0, DefaultSeed))
	conf := RelatedConf
	if opt.Conf != nil {
		conf = *opt.Conf
	}
	buildShown(&res, m, byslug, hits, k, minP, selfScore(s, ps, 0), text, conf)
	return res
}

func relatedScorer() Scorer {
	return &LSAScorer{Label: RelatedScorer, P: DefaultLSAParams, M: 3, Mode: LSAFusion, Lambda: 0.25}
}

// isOwnDescendant reports whether c is the query's own child or one of
// its own playbook runs — layer-0 drops these: a child/run is reachable
// by following the parent/playbook link, not by the lookback nudge.
func isOwnDescendant(q, c TaskData) bool {
	if c.Parent == q.Slug {
		return true
	}
	if c.Kind == "playbook_run" && q.Kind != "playbook_run" && c.Parent == q.Slug {
		return true
	}
	return false
}

// excludesBound reports whether candidate c must be dropped because the
// session is bound to boundSlug: c is that task, or one of its own children
// / playbook runs (the same reach as isOwnDescendant — a direct parent
// link). A bound session never recalls the task it is sitting in, in any
// mode. boundSlug == "" (unbound session, or --include-self) excludes
// nothing.
func excludesBound(c TaskData, boundSlug string) bool {
	if boundSlug == "" {
		return false
	}
	return c.Slug == boundSlug || c.Parent == boundSlug
}

// projectMatcher returns a predicate isolating candidates to one project:
// a project slug (exact project match) or, failing that, a slug prefix.
// nil when no isolation is requested.
func projectMatcher(tasks []TaskData, project string) func(TaskData) bool {
	if project == "" {
		return nil
	}
	isProject := false
	for _, t := range tasks {
		if t.Project == project {
			isProject = true
			break
		}
	}
	if isProject {
		return func(t TaskData) bool { return t.Project == project }
	}
	pre := project + "-"
	return func(t TaskData) bool { return t.Slug == project || strings.HasPrefix(t.Slug, pre) }
}

// summaryBoilerplate are substrings that mark a thin-brief template or a
// deferred placeholder — never the real content.
var summaryBoilerplate = []string{
	"edit this brief freely", "sections to cover", "by composing a flow skill",
	"*deferred", "fill in at task start",
}

// summaryLabels are bare section labels (any heading/emphasis/punctuation
// stripped) that carry no content on their own.
var summaryLabels = map[string]bool{
	"goal": true, "what": true, "why": true, "objective": true, "where": true,
	"done when": true, "out of scope": true, "open questions": true, "open question": true,
	"context": true, "steps": true, "scope": true, "overview": true, "summary": true,
}

// oneLineSummary is the first real sentence of a task's brief — layer-4
// context for the session model. It skips markdown headings, blank lines,
// bare section labels and thin-brief/deferred template boilerplate, then
// joins the first content lines into one sentence (capped in length).
func oneLineSummary(td TaskData) string {
	var sentence []string
	for _, raw := range strings.Split(td.Brief, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			if len(sentence) > 0 {
				break // blank line ends the first paragraph
			}
			continue
		}
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "```") || strings.HasPrefix(line, "|") {
			continue
		}
		low := strings.ToLower(line)
		boiler := false
		for _, b := range summaryBoilerplate {
			if strings.Contains(low, b) {
				boiler = true
				break
			}
		}
		if boiler {
			continue
		}
		clean := strings.TrimSpace(strings.TrimLeft(line, "-*>• "))
		label := strings.ToLower(strings.Trim(clean, " .:*`"))
		if summaryLabels[label] {
			continue
		}
		sentence = append(sentence, clean)
		// Stop at a sentence terminator.
		if strings.ContainsAny(clean, ".!?") {
			break
		}
	}
	out := strings.Join(sentence, " ")
	if i := strings.IndexAny(out, ".!?"); i >= 0 && i+1 < len(out) {
		out = out[:i+1] // first sentence only
	}
	if len([]rune(out)) > 160 {
		out = string([]rune(out)[:159]) + "…"
	}
	return out
}
