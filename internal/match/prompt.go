package match

import (
	"math"
	"sort"
)

// ---------- prompt-based lookback ----------
//
// `flow match related` normally takes a task slug and ranks past work
// against that task's name+brief. Two more entry points let the session
// drive the nudge from text it already has:
//
//   free text  RelatedQuery with Self==""      — rank all tasks against a
//              typed query, no bound task.
//   in-session RelatedQuery with Self==<slug>  — rank all OTHER tasks
//              against a mid-session prompt (heavy) plus the bound task's
//              name+brief (light context, scaled by ContextWeight).
//
// Both reuse the shipped layered pipeline (layer 0 filters, layer 2
// bm25+0.25lsa+card, layer 3 confidence, layer 4 one-liners). Short,
// content-free prompts abstain instead of guessing, and — like every
// mode — items with no meaningful lexical/identity overlap are dropped,
// never padded in to fill k.

// AbstainRareDF is the document-frequency share at which a single term counts
// as distinctive: it anchors the abstain informativeness bar (abstainInfoT) at
// one such term's IDF. A prompt is informative enough to rank when it names an
// identifier OR its in-corpus terms' summed IDF reaches that bar. A word the
// user has never worked on (DF 0, e.g. filler like "ahead") carries no IDF
// mass here and is only admitted via the identifier shape.
const AbstainRareDF = DistinctDF // <= 5% DF: a distinctive term the user rarely touches

// RelatedContextWeight is the default weight the in-session nudge puts on
// the bound task's name+brief, relative to the prompt (weight 1); the
// blended score is score(prompt) + weight·score(brief). On the prompt-mode
// lookback sweep more context was monotonically better on in-session
// prompts — the brief is the
// stronger lookback signal and the prompt only perturbs it — so of the
// swept {0,.25,.5,1} the heaviest (1.0) ranked best (mention MRR .581 vs
// .387 prompt-only; brief-only .650 is the ceiling, i.e. slug mode). The
// prompt's distinct value is for free-text and off-task prompts the eval
// cannot sample, so the default keeps the prompt an equal partner, not a
// faint add.
const RelatedContextWeight = 1.0

// RelatedPromptConf is the calibrated layer-3 confidence fit for PROMPT /
// free-text queries, from the prompt-mode lookback eval (the report's
// "Shipped calibrated fit", strict truth, recommended weight 1.0). The
// shipped RelatedConf was fit on brief-length queries and is clearly off on
// prompts (precision 0.27 vs the 0.65 target), so the prompt modes use this
// refit. n is small (~360 prompt queries, 31 tasks) so calibration is
// provisional; refit and paste again when the ranker, the default weight or
// the corpus changes materially.
// The fifth (query-coverage) feature is carried from the brief-mode fit
// (RelatedConf): coverage is a model-level signal on the same scale whatever
// the query shape, and the prompt-mode confidence eval is not in the shipped
// dev harness. It only sharpens the "one strong word is not strong" direction,
// so it is safe here; refit the whole model if a prompt-mode confidence eval
// is reintroduced.
var RelatedPromptConf = ConfModel{
	Mean: []float64{0.2901204524545236, 1.5104412573075638, 0.02297661299049848, 5.569180125576024, 0.18477},
	Std:  []float64{0.17033795356671208, 0.6954074984764778, 0.05321361126215765, 0.9959913015489906, 0.184321},
	W:    []float64{0.4989919463883737, -0.679776069325615, -0.0805759316340316, 0.5602957663855564, 0.538571},
	B:    -2.9067586185967906,
}

// The prompt and the bound task's brief are combined at the SCORE level,
// not the query level: the shipped ranker's dominant BM25 component is
// set-based (presence, not query-side weight) and its LSA projection uses
// sublinear log-tf, so a single merged query cannot express "heavy prompt,
// light brief". Instead each is scored as its own query and the context
// weight scales the brief's contribution:
//
//	combined(c) = score(prompt, c) + ctxW · score(brief, c)
//
// With ctxW=0 the brief drops out (prompt only); larger ctxW tilts toward
// the brief; brief-only is score(brief, c) alone. This keeps the weight a
// genuine, continuous knob over the shipped scorer.

// bm25QueryRec builds a QueryRec carrying only each candidate's BM25
// signal — all the shipped CardScorer (BM25 + 0.25·latent + card) reads per
// candidate. It skips the model's cosine/kNN/coverage, which the scorer
// never uses, so a large prompt sweep stays cheap.
func bm25QueryRec(m *Model, owner string, q Query, cands []string) QueryRec {
	qr := QueryRec{Owner: owner, Q: q, Cands: cands, sig: make(map[string]Signals, len(cands))}
	for _, c := range cands {
		qr.sig[c] = Signals{BM25: m.BM25(q, c)}
	}
	return qr
}

// blendRank ranks cands by score(query pi) + ctxW·score(query bi). bi < 0
// (or ctxW == 0) scores query pi alone. With floor set, candidates with no
// meaningful overlap are dropped (the display rule); the eval leaves it off
// so metrics are measured over the full ranking. Both queries must be bound
// on ps and carry BM25 signals for every candidate.
func blendRank(m *Model, s *CardScorer, ps *PairSet, pi, bi int, ctxW float64, cands []string, seed uint64, floor bool) []LBHit {
	useB := bi >= 0 && ctxW != 0
	out := make([]LBHit, 0, len(cands))
	for _, c := range cands {
		sc := s.Score(ps.Queries[pi].pair(pi, c, false))
		if useB {
			sc += ctxW * s.Score(ps.Queries[bi].pair(bi, c, false))
		}
		if floor {
			ok := meaningfulOverlapAt(m, s, ps, pi, c)
			if !ok && useB {
				ok = meaningfulOverlapAt(m, s, ps, bi, c)
			}
			if !ok {
				continue
			}
		}
		// Coverage of the primary (prompt/free-text) query, blended with the
		// context query's coverage so an in-session hit that the brief covers
		// is not under-credited.
		cov := ps.Queries[pi].sig[c].Cov
		if useB {
			if bc := ps.Queries[bi].sig[c].Cov; bc > cov {
				cov = bc
			}
		}
		out = append(out, LBHit{Slug: c, Score: math.Round(sc*1e9) / 1e9, Cov: cov})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return slugHash(seed, out[i].Slug) < slugHash(seed, out[j].Slug)
	})
	return out
}

// blendSelf is the per-query score normaliser: the blended score of the
// query against its own (bound) task.
func blendSelf(m *Model, s *CardScorer, ps *PairSet, pi, bi int, ctxW float64, owner string) float64 {
	sc := s.Score(queryOwnerPair(m, pi, ps.Queries[pi].Q, owner))
	if bi >= 0 && ctxW != 0 {
		sc += ctxW * s.Score(queryOwnerPair(m, bi, ps.Queries[bi].Q, owner))
	}
	return math.Round(sc*1e9) / 1e9
}

// queryOwnerPair builds the (query, owner) pair with the owner's BM25
// signal, mirroring selfScore for the blended scorer.
func queryOwnerPair(m *Model, qi int, q Query, owner string) Pair {
	sg := m.SignalsFor(q, owner)
	return Pair{Query: qi, Target: owner, A: Assessment{Target: owner, Tokens: q.Tokens, Rank: 1,
		Sim: sg.Sim, KNN: sg.KNN, Cov: sg.Cov, BM25: sg.BM25}}
}

// meaningfulOverlapAt reports whether query qi (bound on ps) shares a
// distinctive term or a card entity with slug.
func meaningfulOverlapAt(m *Model, s *CardScorer, ps *PairSet, qi int, slug string) bool {
	if s != nil && s.card(Pair{Query: qi, Target: slug}) > 0 {
		return true
	}
	e := m.Tasks[slug]
	if e == nil {
		return false
	}
	n := float64(len(m.Tasks))
	if n == 0 {
		return false
	}
	terms := e.termStrings(m)
	for t := range ps.Queries[qi].Q.tf {
		if terms[t] && float64(m.dfOf(t))/n <= CommonDF {
			return true
		}
	}
	return false
}

// hasIdentifierTerm reports whether a prompt names an identifier-shaped
// term: a compound, a mixed-alnum id, an ALLCAPS or mid-sentence
// Capitalized entity (a slug / service / customer / ticket).
func hasIdentifierTerm(prompt string) bool {
	for _, c := range EntityTerms(prompt, false) {
		if c.shape {
			return true
		}
	}
	return false
}

// promptAbstains decides whether a prompt is too generic to rank: it has
// no identifier and not enough distinctive vocabulary against this corpus.
// The test is informativeness, not length: "logical replication" and "send
// invoices" rank, while "fix it" and "go ahead" abstain (generic / unseen
// words only).
//
// Informativeness is the summed IDF of the prompt's in-corpus unigram terms
// (terms the user has never worked on, DF 0, carry no evidence of relevant
// past work and only count through the identifier shape). A prompt clears
// the bar when that mass reaches abstainInfoT — the IDF of a single term at
// the rare-DF threshold, i.e. the least a lone distinctive term used to
// bring. This is a smooth replacement for the earlier hard DF cliffs (one
// term under 5% OR two under 30%), which abstained on real queries that
// straddled a boundary: "redis data migration" (redis ≈ 5.3% DF just over
// the 5% cliff, migration ≈ 32% just over the 30% cliff) wrongly scored as
// one mid term and abstained, while "redis data copy" ranked. Summed IDF has
// no cliff, so both now clear the bar and surface the same redis tasks.
func (m *Model) promptAbstains(prompt string) bool {
	if hasIdentifierTerm(prompt) {
		return false
	}
	n := float64(len(m.Tasks))
	if n == 0 {
		return true
	}
	var info float64
	for t := range Counts(prompt, LookbackQueryTokens) {
		if IsBigram(t) || m.dfOf(t) < 1 || m.stoppedTerm(t) {
			continue
		}
		info += m.idf(t)
	}
	return info < abstainInfoT(n)
}

// abstainInfoT is the informativeness bar promptAbstains uses: the IDF of a
// single term sitting exactly at the rare-DF threshold (AbstainRareDF). A
// prompt whose in-corpus terms sum to at least this much IDF carries at least
// one distinctive term's worth of signal. It tracks the model's idf formula
// so the two stay consistent as the corpus grows.
func abstainInfoT(n float64) float64 {
	rareDF := math.Round(AbstainRareDF * n)
	return math.Log((n+1)/(rareDF+1)) + 1
}

// RelatedQueryOptions configures the prompt / free-text nudge.
type RelatedQueryOptions struct {
	Prompt        string  // the user's free text / mid-session prompt (heavy weight)
	Self          string  // in-session mode: the bound task slug (context + excluded + self-normaliser); "" = free-text
	ContextWeight float64 // weight on the bound task's name+brief (in-session mode)
	K             int     // layer 4: how many one-liners to emit
	MinP          float64 // layer 3: confidence cut (0 = plain top k, still overlap-gated)
	// Layer 0 hard filters:
	ExcludeChildren bool   // in-session: drop the bound task's own children / playbook runs
	Project         string // isolate to a project slug or a slug prefix ("" = none)
	// ExcludeBound, when set, drops the session's bound task and its own
	// children / playbook runs from the candidates. Free-text mode (Self=="")
	// has no self to exclude, so this is how a bound session keeps the task it
	// is sitting in out of a free-text recall. "" = no bound exclusion.
	ExcludeBound string
	// Pre-built disk-cache artifacts (nil = build/fit on the fly).
	Space *LSASpace
	Model *Model
	Cards *CardIndex
	// Conf overrides the confidence model (default RelatedPromptConf).
	Conf *ConfModel
}

// RelatedQuery runs the layered pipeline for a typed query or a mid-session
// prompt. With Self=="" it is free text over every task; with Self set it
// excludes that task (and optionally its children) and folds the task's
// name+brief in as light context. A content-free short prompt abstains.
func RelatedQuery(tasks []TaskData, opt RelatedQueryOptions) RelatedResult {
	mode := "query"
	if opt.Self != "" {
		mode = "prompt"
	}
	res := RelatedResult{Task: opt.Self, Scorer: ShippedRelatedScorer, Mode: mode, MinP: opt.MinP, Project: opt.Project}

	m := opt.Model
	if m == nil {
		m = NewModel(DefaultWeights)
		for _, t := range tasks {
			m.Upsert(t)
		}
	}

	// ---- abstain: too generic / content-free to rank (brief §3) ----
	if m.promptAbstains(opt.Prompt) {
		res.Abstain = true
		return res
	}
	byslug := make(map[string]TaskData, len(tasks))
	var self *TaskData
	for i := range tasks {
		byslug[tasks[i].Slug] = tasks[i]
		if opt.Self != "" && tasks[i].Slug == opt.Self {
			self = &tasks[i]
		}
	}
	context := ""
	if self != nil {
		context = self.Name + "\n" + self.Brief
	}
	ctxW := opt.ContextWeight

	// ---- layer 0: hard filters ----
	cust := projectMatcher(tasks, opt.Project)
	var cands []string
	for _, t := range tasks {
		if opt.Self != "" {
			if t.Slug == opt.Self {
				continue
			}
			if opt.ExcludeChildren && self != nil && isOwnDescendant(*self, t) {
				continue
			}
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

	// ---- build the prompt query (heavy) and, in-session, the brief query
	// (scored separately and blended at ctxW) ----
	tc := &termCache{m: m, t: map[string]map[string]bool{}}
	ps := &PairSet{Model: m, Corpus: tasks}
	qp := newQueryRec(m, tc, opt.Self, lbQuery(m, opt.Prompt), cands)
	qp.text = opt.Prompt
	ps.Queries = []QueryRec{qp}
	bi := -1
	qText := opt.Prompt
	if context != "" && ctxW != 0 {
		qb := newQueryRec(m, tc, opt.Self, lbQuery(m, context), cands)
		qb.text = context
		ps.Queries = append(ps.Queries, qb)
		bi = 1
		qText = opt.Prompt + "\n" + context
	}
	if opt.Space != nil {
		opt.Space.Reindex()
	}
	cards := opt.Cards
	if cards == nil {
		cards = BuildRelatedCards(tasks)
	}
	s := shippedRelatedScorer(cards, opt.Space)
	bindTo(s, ps)
	hits := blendRank(m, s, ps, 0, bi, ctxW, cands, DefaultSeed, true)

	// Self normaliser for score/self: the bound task for in-session mode,
	// the best achievable score for free text (no anchor task).
	self0 := 0.0
	if opt.Self != "" {
		self0 = blendSelf(m, s, ps, 0, bi, ctxW, opt.Self)
	} else if len(hits) > 0 {
		self0 = hits[0].Score
	}
	// Confidence model: in-session prompts carry the brief as context (long
	// query), so they use the prompt-mode refit; a free-text query is a short
	// standalone search, closer to a brief, so it uses the shipped brief-fit
	// RelatedConf (the prompt refit's log-tokens feature over-penalises short
	// queries). The eval measures only the in-session case.
	conf := RelatedConf
	if opt.Self != "" {
		conf = RelatedPromptConf
	}
	if opt.Conf != nil {
		conf = *opt.Conf
	}
	buildShown(&res, m, byslug, hits, opt.K, opt.MinP, self0, qText, conf)
	return res
}

// filterOverlap drops hits with no MEANINGFUL overlap: a shared query term
// that is at least somewhat distinctive (document frequency below CommonDF,
// so a template word like "goal"/"done"/"scope" that appears in nearly
// every brief does not count), or a shared identity-card entity. This is
// the "never pad zeros" rule (driver feedback): a task whose only tie to
// the query is ubiquitous boilerplate is dropped, never shown to fill out
// k. Applies to every mode.
func filterOverlap(m *Model, q Query, s *CardScorer, hits []LBHit) []LBHit {
	out := make([]LBHit, 0, len(hits))
	for _, h := range hits {
		if meaningfulOverlap(m, q, s, h.Slug) {
			out = append(out, h)
		}
	}
	return out
}

// meaningfulOverlap reports whether slug shares a distinctive term or a
// card entity with the query.
func meaningfulOverlap(m *Model, q Query, s *CardScorer, slug string) bool {
	e := m.Tasks[slug]
	if e == nil {
		return false
	}
	if s != nil && s.card(Pair{Target: slug}) > 0 {
		return true
	}
	n := float64(len(m.Tasks))
	if n == 0 {
		return false
	}
	terms := e.termStrings(m)
	for t := range q.tf {
		if terms[t] && float64(m.dfOf(t))/n <= CommonDF {
			return true
		}
	}
	return false
}

// buildShown applies the layer-3 confidence cut to the (already
// overlap-filtered) hits and fills res.Top / res.Confident with layer-4
// one-liners. confText is the query text whose token count feeds the
// confidence model; self is the per-query score normaliser.
func buildShown(res *RelatedResult, m *Model, byslug map[string]TaskData, hits []LBHit,
	k int, minP, self float64, confText string, conf ConfModel) {
	res.Self = self
	l := &ConfList{Query: res.Task, Tokens: len(Tokenize(confText)), Self: self}
	for _, h := range hits[:min(max(k, ConfTopN), len(hits))] {
		l.Items = append(l.Items, ConfItem{Slug: h.Slug, Score: h.Score, Cov: h.Cov})
	}
	for i, it := range l.Items {
		if i >= k {
			break
		}
		p := conf.Prob(l, i)
		if minP > 0 && p < minP {
			break
		}
		lab := LabelPossible
		switch {
		case p >= RelatedStrongP:
			lab = LabelStrong
		case p < RelatedMinP:
			lab = LabelWeak
		}
		td := byslug[it.Slug]
		hit := RelatedHit{Slug: it.Slug, Score: it.Score, P: p, Label: lab,
			Status: m.Tasks[it.Slug].StatusLabel(), Name: td.Name, Summary: oneLineSummary(td)}
		res.Top = append(res.Top, hit)
		if p >= RelatedStrongP {
			res.Confident = append(res.Confident, hit)
		}
	}
}
