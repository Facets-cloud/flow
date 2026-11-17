package match

import (
	"math"
	"sort"
)

// ModelVersion bumps whenever the on-disk format or the feature
// definition changes; a mismatched file must be retrained.
const ModelVersion = 4

// MinQueryTokens is the fewest informative tokens a prompt needs before
// the model will judge it. Shorter prompts ("status?", "go", "yes")
// carry no topical signal and are reported as abstentions.
const MinQueryTokens = 2

// Update is one dated progress note.
type Update struct {
	Date string `json:"date"` // YYYY-MM-DD from the filename, "" if unknown
	Text string `json:"-"`
}

// TaskData is everything the model consumes for one task, as loaded
// from flow's on-disk state. Prompts are already cleaned and in
// chronological order.
type TaskData struct {
	Slug           string
	Name           string
	Status         string
	Archived       bool
	CreatedAt      string
	DoneAt         string // status_changed_at when status=done
	SessionStarted string
	Brief          string
	Updates        []Update
	Prompts        []string
	Fingerprint    string // change detector for incremental update
	// Structure: DERIVES lookback truth (lookback_truth.go). Only the
	// structure-prior experiment (structure.go) also reads it as a
	// feature, and is then scored on mention truth only.
	Project  string
	Parent   string
	Kind     string // regular | playbook_run
	Playbook string
	Tags     []string
	TagAt    map[string]string // tag -> when it was attached (UTC RFC3339), if known
}

// Weights scale each field's sublinear term frequency in a task's
// document vector.
type Weights struct {
	Name    float64 `json:"name"`
	Brief   float64 `json:"brief"`
	Updates float64 `json:"updates"`
	Prompts float64 `json:"prompts"`
}

// DefaultWeights weigh every field equally. A sweep on real history
// (name/brief/updates/prompts in {0..5}) moved ROC-AUC by < 0.01; uniform
// weights gave the best top-1 retrieval, so they are the default.
var DefaultWeights = Weights{Name: 1, Brief: 1, Updates: 1, Prompts: 1}

const (
	fName = iota
	fBrief
	fUpdates
	fPrompts
	numFields
)

// idCounts is a task field's (interned term id -> count) map. Terms are
// interned to int32 against the model's one vocabulary, and the per-field
// counts are stored as float32: they are small integers (Counts() emits
// TF counts), so float32 holds them exactly while halving the footprint of
// the index — which, at ~240k distinct terms over a few hundred tasks, is
// the dominant resident cost. Scoring (taskVec, BM25, docLen) still
// accumulates in float64; only storage is narrowed.
type idCounts = map[int32]float32

// TaskEntry is one task's indexed representation.
type TaskEntry struct {
	Slug           string              `json:"slug"`
	Name           string              `json:"name"`
	Status         string              `json:"status"`
	Archived       bool                `json:"archived,omitempty"`
	CreatedAt      string              `json:"created_at"`
	DoneAt         string              `json:"done_at,omitempty"`
	SessionStarted string              `json:"session_started,omitempty"`
	Fingerprint    string              `json:"fingerprint"`
	NumPrompts     int                 `json:"num_prompts"`
	Fields         [numFields]idCounts `json:"fields"`
	// PromptTF keeps the most recent informative prompts individually
	// for the nearest-past-prompt (kNN) feature.
	PromptTF []idCounts `json:"prompt_tf,omitempty"`
	// DocLen is the field-weighted token count (BM25 document length).
	DocLen float64 `json:"doc_len"`
}

// MaxKNNPrompts bounds how many individual past prompts a task keeps.
const MaxKNNPrompts = 300

// Model is the persisted relevance model.
//
// Terms are interned to int32 ids against one shared vocabulary (Terms /
// vocab): every per-task field map and the DF table are keyed by id, not
// by string, so the ~240k distinct term strings are stored exactly once
// (in Terms) instead of being replicated across every task's maps. Queries
// stay in string space and resolve to ids at scoring time, so the ranking
// math is unchanged.
type Model struct {
	Version   int                   `json:"version"`
	TrainedAt string                `json:"trained_at"`
	UpdatedAt string                `json:"updated_at,omitempty"`
	Watermark string                `json:"watermark"` // newest source timestamp folded in
	Weights   Weights               `json:"weights"`
	Tasks     map[string]*TaskEntry `json:"tasks"`
	Terms     []string              `json:"terms"`     // id -> term (the one vocabulary)
	DF        map[int32]int         `json:"df"`        // id -> document frequency
	TotalLen  float64               `json:"total_len"` // sum of task DocLen, for BM25's avgdl

	vocab    map[string]int32               // term -> id, rebuilt from Terms on load
	big      []bool                         // id -> IsBigram(Terms[id]), rebuilt lazily
	bm25tf   map[string]map[int32]float64   // cache: field-weighted raw tf per task
	vecs     map[string]map[int32]float64   // cache, invalidated on any mutation
	knnVecs  map[string][]map[int32]float64 // cache, invalidated on any mutation
	termSets map[string]map[int32]bool      // cache, invalidated on any mutation
	rev      int                            // bumped on every mutation (derived-index invalidation)
}

// vec is a query's string-keyed TF-IDF vector (queries stay in string
// space; only the indexed tasks are interned).
type vec map[string]float64

// NewModel returns an empty model with the given field weights.
func NewModel(w Weights) *Model {
	return &Model{Version: ModelVersion, Weights: w, Tasks: map[string]*TaskEntry{},
		DF: map[int32]int{}, vocab: map[string]int32{}}
}

// Reindex rebuilds the unexported term->id vocabulary and bigram flags
// after a gob load (which restores Terms but not the derived indexes).
// Call it once after restoring a Model from disk.
func (m *Model) Reindex() {
	m.vocab = nil
	m.big = nil
	m.ensureVocab()
	m.ensureBig()
}

// ensureVocab rebuilds the term->id map after a gob load (which restores
// Terms but not the unexported vocab). Idempotent and cheap.
func (m *Model) ensureVocab() {
	if m.vocab != nil {
		return
	}
	m.vocab = make(map[string]int32, len(m.Terms))
	for i, t := range m.Terms {
		m.vocab[t] = int32(i)
	}
}

// ensureBig aligns the per-id bigram flag with Terms (it is not persisted).
func (m *Model) ensureBig() {
	for len(m.big) < len(m.Terms) {
		m.big = append(m.big, IsBigram(m.Terms[len(m.big)]))
	}
}

// intern maps a term to its id, assigning a fresh one on first sight.
func (m *Model) intern(t string) int32 {
	m.ensureVocab()
	if id, ok := m.vocab[t]; ok {
		return id
	}
	m.ensureBig()
	id := int32(len(m.Terms))
	m.vocab[t] = id
	m.Terms = append(m.Terms, t)
	m.big = append(m.big, IsBigram(t))
	return id
}

// lookup resolves a query term to its id; ok=false for an out-of-vocab
// term (one the corpus has never contained).
func (m *Model) lookup(t string) (int32, bool) {
	m.ensureVocab()
	id, ok := m.vocab[t]
	return id, ok
}

// internCounts interns a string TF map into an id-keyed, float32 one.
func (m *Model) internCounts(c map[string]float64) idCounts {
	out := make(idCounts, len(c))
	for t, v := range c {
		out[m.intern(t)] = float32(v)
	}
	return out
}

func (m *Model) entryFrom(td TaskData) *TaskEntry {
	e := &TaskEntry{
		Slug: td.Slug, Name: td.Name, Status: td.Status, Archived: td.Archived,
		CreatedAt: td.CreatedAt, DoneAt: td.DoneAt, SessionStarted: td.SessionStarted,
		Fingerprint: td.Fingerprint,
	}
	e.Fields[fName] = m.internCounts(Counts(td.Name+" "+td.Slug, 0))
	e.Fields[fBrief] = m.internCounts(Counts(td.Brief, 0))
	up := idCounts{}
	for _, u := range td.Updates {
		for t, c := range Counts(u.Text, 0) {
			up[m.intern(t)] += float32(c)
		}
	}
	e.Fields[fUpdates] = up
	pr := idCounts{}
	for _, p := range td.Prompts {
		c := Counts(p, MaxPromptTokens)
		if len(c) > 0 {
			e.NumPrompts++
		}
		if sumCounts(c) >= MinQueryTokens {
			e.PromptTF = append(e.PromptTF, m.internCounts(c))
		}
		for t, n := range c {
			pr[m.intern(t)] += float32(n)
		}
	}
	e.Fields[fPrompts] = pr
	if len(e.PromptTF) > MaxKNNPrompts {
		e.PromptTF = e.PromptTF[len(e.PromptTF)-MaxKNNPrompts:]
	}
	return e
}

func sumCounts(c map[string]float64) int {
	n := 0
	for t, k := range c {
		if !IsBigram(t) {
			n += int(k)
		}
	}
	return n
}

// termIDs is the set of interned term ids a task contains.
func (e *TaskEntry) termIDs() map[int32]bool {
	s := map[int32]bool{}
	for _, f := range e.Fields {
		for t := range f {
			s[t] = true
		}
	}
	return s
}

// termStrings reconstructs a task's term set in string space, for the few
// eval / prompt-pipeline consumers that compare against string query
// terms (baselines, common-term filters).
func (e *TaskEntry) termStrings(m *Model) map[string]bool {
	s := make(map[string]bool)
	for _, f := range e.Fields {
		for id := range f {
			s[m.Terms[id]] = true
		}
	}
	return s
}

// Upsert adds or replaces a task, maintaining DF exactly.
func (m *Model) Upsert(td TaskData) {
	m.Remove(td.Slug)
	e := m.entryFrom(td)
	for t := range e.termIDs() {
		m.DF[t]++
	}
	e.DocLen = m.docLen(e)
	m.TotalLen += e.DocLen
	m.Tasks[td.Slug] = e
	m.rev++
	m.vecs, m.knnVecs, m.termSets, m.bm25tf = nil, nil, nil, nil
}

// UpsertKeepingPrompts re-indexes a task from td's name/brief/updates but
// KEEPS the existing entry's pooled prompt terms (and kNN vectors). It is
// for the incremental cache: a live task's transcript keeps growing, but
// re-decoding it to re-tokenize the prompts costs seconds, and that task
// is almost always the one being queried (excluded from its own
// candidates), so a slightly stale prompt index does not affect which
// PAST tasks rank as related. td should carry no prompts.
func (m *Model) UpsertKeepingPrompts(td TaskData) {
	old := m.Tasks[td.Slug]
	e := m.entryFrom(td) // td has no prompts -> empty prompt field
	if old != nil {
		e.Fields[fPrompts] = old.Fields[fPrompts]
		e.PromptTF = old.PromptTF
		e.NumPrompts = old.NumPrompts
	}
	m.Remove(td.Slug)
	for t := range e.termIDs() {
		m.DF[t]++
	}
	e.DocLen = m.docLen(e)
	m.TotalLen += e.DocLen
	m.Tasks[td.Slug] = e
	m.rev++
	m.vecs, m.knnVecs, m.termSets, m.bm25tf = nil, nil, nil, nil
}

// Remove drops a task (no-op if absent), maintaining DF exactly.
func (m *Model) Remove(slug string) {
	old, ok := m.Tasks[slug]
	if !ok {
		return
	}
	for t := range old.termIDs() {
		if m.DF[t]--; m.DF[t] <= 0 {
			delete(m.DF, t)
		}
	}
	m.TotalLen -= old.DocLen
	if len(m.Tasks) == 1 {
		m.TotalLen = 0 // clear float drift when the index empties
	}
	delete(m.Tasks, slug)
	m.rev++
	m.vecs, m.knnVecs, m.termSets, m.bm25tf = nil, nil, nil, nil
}

// StopDF is the auto-stoplist cut: a term present in more than this
// fraction of tasks is flow boilerplate (task/goal/brief/done/…) and is
// dropped from lexical scoring and the coverage / informativeness math.
// 0 disables the stoplist. Measured on the lookback eval; see docs/recall.md.
var StopDF = 0.40

// stopMinTasks guards the stoplist on tiny histories: with only a handful
// of tasks, a 40%-DF term is not boilerplate, just shared vocabulary, so a
// new user's index is scored exactly as before.
const stopMinTasks = 25

// stopped reports whether an interned term is on the auto-stoplist.
func (m *Model) stopped(id int32) bool {
	n := len(m.Tasks)
	return StopDF > 0 && n >= stopMinTasks && float64(m.DF[id]) > StopDF*float64(n)
}

// stoppedTerm reports whether a query term (string) is on the auto-stoplist.
func (m *Model) stoppedTerm(t string) bool {
	if id, ok := m.lookup(t); ok {
		return m.stopped(id)
	}
	return false
}

func (m *Model) idfID(id int32) float64 {
	n := float64(len(m.Tasks))
	return math.Log((n+1)/(float64(m.DF[id])+1)) + 1
}

// idf is the inverse document frequency of a query term (string space); an
// out-of-vocab term is treated as DF 0, exactly as a missing map key was.
func (m *Model) idf(t string) float64 {
	if id, ok := m.lookup(t); ok {
		return m.idfID(id)
	}
	n := float64(len(m.Tasks))
	return math.Log(n+1) + 1
}

// dfOf is the document frequency of a query term (0 if out of vocabulary).
func (m *Model) dfOf(t string) int {
	if id, ok := m.lookup(t); ok {
		return m.DF[id]
	}
	return 0
}

func (m *Model) fieldWeight(f int) float64 {
	switch f {
	case fName:
		return m.Weights.Name
	case fBrief:
		return m.Weights.Brief
	case fUpdates:
		return m.Weights.Updates
	default:
		return m.Weights.Prompts
	}
}

func (m *Model) taskVec(slug string) map[int32]float64 {
	if m.vecs == nil {
		m.vecs = map[string]map[int32]float64{}
	}
	if v, ok := m.vecs[slug]; ok {
		return v
	}
	e := m.Tasks[slug]
	if e == nil {
		return nil
	}
	v := map[int32]float64{}
	for f, counts := range e.Fields {
		w := m.fieldWeight(f)
		if w == 0 {
			continue
		}
		for t, c := range counts {
			v[t] += w * (1 + math.Log(float64(c)))
		}
	}
	normalizeID(v, m.idfID)
	m.vecs[slug] = v
	return v
}

// normalizeID applies IDF weighting and L2-normalizes an id-keyed vector.
func normalizeID(v map[int32]float64, idf func(int32) float64) {
	var ss float64
	for t, x := range v {
		x *= idf(t)
		v[t] = x
		ss += x * x
	}
	if ss == 0 {
		return
	}
	n := math.Sqrt(ss)
	for t := range v {
		v[t] /= n
	}
}

func normalize(v vec, idf func(string) float64) {
	var ss float64
	for t, x := range v {
		x *= idf(t)
		v[t] = x
		ss += x * x
	}
	if ss == 0 {
		return
	}
	n := math.Sqrt(ss)
	for t := range v {
		v[t] /= n
	}
}

// Query is a tokenized, IDF-weighted, normalized prompt.
type Query struct {
	v      vec
	tf     map[string]float64
	Tokens int // informative token count
}

// NewQuery vectorizes a (cleaned) prompt against the model's IDF.
func (m *Model) NewQuery(prompt string) Query {
	return m.queryFromCounts(Counts(prompt, MaxPromptTokens))
}

// Requery re-weights an existing query under this model's IDF.
func (m *Model) Requery(q Query) Query { return m.queryFromCounts(q.tf) }

func (m *Model) queryFromCounts(c map[string]float64) Query {
	n := 0
	v := vec{}
	for t, k := range c {
		if !IsBigram(t) {
			n += int(k)
		}
		v[t] = 1 + math.Log(k)
	}
	normalize(v, m.idf)
	return Query{v: v, tf: c, Tokens: n}
}

// Sim is the cosine similarity between a query and a task document.
func (m *Model) Sim(q Query, slug string) float64 {
	d := m.taskVec(slug)
	var s float64
	for t, x := range q.v {
		if id, ok := m.lookup(t); ok && !m.stopped(id) {
			s += x * d[id]
		}
	}
	return s
}

// KNNTop is how many nearest past prompts the kNN feature averages.
const KNNTop = 3

// KNN is the mean cosine between a query and the task's KNNTop most
// similar single past prompts (missing slots count as 0). Averaging,
// rather than taking the single best, keeps one stray earlier prompt that
// happens to share a rare term from dominating short queries.
func (m *Model) KNN(q Query, slug string) float64 {
	if m.knnVecs == nil {
		m.knnVecs = map[string][]map[int32]float64{}
	}
	vs, ok := m.knnVecs[slug]
	if !ok {
		e := m.Tasks[slug]
		if e == nil {
			return 0
		}
		for _, c := range e.PromptTF {
			v := map[int32]float64{}
			for t, k := range c {
				v[t] = 1 + math.Log(float64(k))
			}
			normalizeID(v, m.idfID)
			vs = append(vs, v)
		}
		m.knnVecs[slug] = vs
	}
	var best [KNNTop]float64
	for _, v := range vs {
		var s float64
		for t, x := range q.v {
			if id, ok := m.lookup(t); ok {
				s += x * v[id]
			}
		}
		for i := range best {
			if s > best[i] {
				copy(best[i+1:], best[i:KNNTop-1])
				best[i] = s
				break
			}
		}
	}
	var sum float64
	for _, b := range best {
		sum += b
	}
	return sum / KNNTop
}

func (m *Model) docLen(e *TaskEntry) float64 {
	m.ensureBig()
	var n float64
	for f, counts := range e.Fields {
		w := m.fieldWeight(f)
		for t, c := range counts {
			if !m.big[t] {
				n += w * float64(c)
			}
		}
	}
	return n
}

// BM25 parameters (standard Robertson/Sparck-Jones defaults).
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

func (m *Model) bm25idfID(id int32) float64 {
	n, df := float64(len(m.Tasks)), float64(m.DF[id])
	return math.Log(1 + (n-df+0.5)/(df+0.5))
}

// bm25idf is the BM25 IDF of a query term (string space); an out-of-vocab
// term is DF 0, exactly as a missing map key was.
func (m *Model) bm25idf(t string) float64 {
	if id, ok := m.lookup(t); ok {
		return m.bm25idfID(id)
	}
	n := float64(len(m.Tasks))
	return math.Log(1 + (n+0.5)/0.5)
}

// BM25 is the Okapi BM25 score of the task for the query's distinct
// terms, normalized by the query's maximum attainable score so it lies in
// [0,1). Unlike cosine it saturates term frequency and normalizes length
// against the average task, so a long task that mentions the query's
// phrase dozens of times is not diluted by the rest of its history.
func (m *Model) BM25(q Query, slug string) float64 {
	if m.bm25tf == nil {
		m.bm25tf = map[string]map[int32]float64{}
	}
	tf, ok := m.bm25tf[slug]
	if !ok {
		e := m.Tasks[slug]
		if e == nil {
			return 0
		}
		tf = map[int32]float64{}
		for f, counts := range e.Fields {
			w := m.fieldWeight(f)
			for t, c := range counts {
				tf[t] += w * float64(c)
			}
		}
		m.bm25tf[slug] = tf
	}
	e := m.Tasks[slug]
	avg := m.TotalLen / float64(len(m.Tasks))
	if avg <= 0 {
		avg = 1
	}
	norm := bm25K1 * (1 - bm25B + bm25B*e.DocLen/avg)
	var s, max float64
	for t := range q.tf {
		id, ok := m.lookup(t)
		if ok && m.stopped(id) {
			continue // boilerplate: contributes to neither score nor the cap
		}
		w := m.bm25idf(t)
		max += w * (bm25K1 + 1)
		if !ok {
			continue
		}
		if f := tf[id]; f > 0 {
			s += w * f * (bm25K1 + 1) / (f + norm)
		}
	}
	if max == 0 {
		return 0
	}
	return s / max
}

// Coverage is the IDF-weighted share of the query's terms that occur
// anywhere in the task's document — a recall-style lexical signal that
// is insensitive to how long the task's history is.
func (m *Model) Coverage(q Query, slug string) float64 {
	if m.termSets == nil {
		m.termSets = map[string]map[int32]bool{}
	}
	ts, ok := m.termSets[slug]
	if !ok {
		e := m.Tasks[slug]
		if e == nil {
			return 0
		}
		ts = e.termIDs()
		m.termSets[slug] = ts
	}
	var hit, tot float64
	for t := range q.tf {
		id, ok := m.lookup(t)
		if ok && m.stopped(id) {
			continue // boilerplate: out of the coverage math
		}
		w := m.idf(t)
		tot += w
		if ok && ts[id] {
			hit += w
		}
	}
	if tot == 0 {
		return 0
	}
	return hit / tot
}

// Candidate is a task with its similarity to a query.
type Candidate struct {
	Slug string  `json:"slug"`
	Sim  float64 `json:"sim"`
}

// Rank scores q against every slug and returns them most-similar first
// (ties broken by slug for determinism).
func (m *Model) Rank(q Query, slugs []string) []Candidate {
	out := make([]Candidate, 0, len(slugs))
	for _, s := range slugs {
		if _, ok := m.Tasks[s]; ok {
			out = append(out, Candidate{s, m.Sim(q, s)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sim != out[j].Sim {
			return out[i].Sim > out[j].Sim
		}
		return out[i].Slug < out[j].Slug
	})
	return out
}

// Assessment is the verdict for (prompt, target task).
type Assessment struct {
	Target    string      `json:"target"`
	Sim       float64     `json:"sim"`
	BestOther float64     `json:"best_other"`
	KNN       float64     `json:"knn"`            // nearest single past prompt of target
	KNNOther  float64     `json:"knn_best_other"` // same, best competing task
	Cov       float64     `json:"coverage"`       // IDF-weighted query-term coverage by target
	CovOther  float64     `json:"coverage_best_other"`
	BM25      float64     `json:"bm25"` // normalized BM25 of target
	BM25Other float64     `json:"bm25_best_other"`
	Rank      int         `json:"rank"` // 1-based rank of target among candidates
	Tokens    int         `json:"tokens"`
	Prob      float64     `json:"prob"`
	Belongs   bool        `json:"belongs"`
	Abstain   bool        `json:"abstain"`
	Top       []Candidate `json:"top"`
}

// Signals are the per-(query, task) similarity measurements.
type Signals struct {
	Sim, KNN, Cov, BM25 float64
}

// SignalsFor measures every similarity signal of q against one task.
func (m *Model) SignalsFor(q Query, slug string) Signals {
	return Signals{Sim: m.Sim(q, slug), KNN: m.KNN(q, slug), Cov: m.Coverage(q, slug), BM25: m.BM25(q, slug)}
}

// RankBy selects the retrieval score that orders candidates and defines
// Assessment.Rank and the dominance guard: "bm25" (default) or "cosine".
// Exposed for experiments (`flow match eval --rank-by`).
var RankBy = "bm25"

// Primary is the retrieval score under RankBy.
func (s Signals) Primary() float64 {
	if RankBy == "cosine" {
		return s.Sim
	}
	return s.BM25
}

// Primary is the target's retrieval score under RankBy.
func (a Assessment) Primary() float64 {
	if RankBy == "cosine" {
		return a.Sim
	}
	return a.BM25
}

// StatusLabel is "open", "done" or "archived" for display.
func (e *TaskEntry) StatusLabel() string {
	switch {
	case e.Archived:
		return "archived"
	case e.Status == "done":
		return "done"
	}
	return "open"
}
