package match

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// ---------- identity card (lookback experiment) ----------
//
// The lookback eval's misses are mostly "identifier mismatch": the query
// shares distinctive words with a related past task, but none of that
// task's name/slug words, so pooled BM25 over the whole (long) history
// does not lift it. An identity card is a compact per-task term set —
// name, slug parts and the top distinctive entities (service/repo/env
// ids, customer/org names, ticket ids) from its brief and notes — scored
// on its own against the entities in the query.
//
// Terms are normalized so separator and case variants collide:
// acme_eu_prod, acme-eu-prod, Acme.Eu.Prod and the running words
// "acme eu prod" all yield the compound term "acme-eu-prod" (word runs
// become 2-3 word compounds), and parts are lightly stemmed (plurals).
//
// The card is built from the candidate's own text with every task slug
// stripped (the candidate's own slug comes back through its name), so a
// card never carries another task's identity — in particular not the
// query's — and mention truth cannot leak through it.

// cardTerm is one extracted term's evidence within a text.
type cardTerm struct {
	n     float64 // occurrences
	shape bool    // identifier-shaped: compound, mixed alnum, ALLCAPS, mid-sentence Capitalized
}

// Card weighting knobs.
const (
	cardShapeBoost = 1.5 // identifier-shaped terms over plain words
	cardNameBoost  = 2.0 // terms from the task's name / slug
	cardMaxDF      = 0.10
	cardMaxParts   = 6
)

// segment breaks: punctuation that ends a word run.
func isSegBreak(r rune) bool {
	switch r {
	case ',', ';', '(', ')', '[', ']', '{', '}', '"', '`', '!', '?', '|', '<', '>', '=', '*', '#', '\n', '\r', '\t':
		return true
	}
	return false
}

// normPart lowercases and stems one identifier part; "" if it is not a
// usable word (stopword, number, hex id, single char).
func normPart(p string) string {
	p = strings.ToLower(p)
	if len(p) < 2 || isNumeric(p) || idLike(p) {
		return ""
	}
	hasLetter := false
	for _, r := range p {
		if unicode.IsLetter(r) {
			hasLetter = true
			break
		}
	}
	if !hasLetter {
		return ""
	}
	return stem(p)
}

func mixedAlnum(s string) bool {
	var l, d bool
	for _, r := range s {
		l = l || unicode.IsLetter(r)
		d = d || unicode.IsDigit(r)
	}
	return l && d
}

func allCaps(s string) bool {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			if !unicode.IsUpper(r) {
				return false
			}
			n++
		}
	}
	return n >= 2
}

// EntityTerms extracts normalized entity terms from text: unigram words,
// identifier compounds (parts joined by "-", plus their contiguous
// sub-spans) and, with runs, 2-3 word compounds of adjacent content
// words. Stopwords never enter a term.
func EntityTerms(text string, runs bool) map[string]*cardTerm {
	out := map[string]*cardTerm{}
	add := func(t string, shape bool) {
		if t == "" {
			return
		}
		c := out[t]
		if c == nil {
			c = &cardTerm{}
			out[t] = c
		}
		c.n++
		c.shape = c.shape || shape
	}
	addCompound := func(parts []string, shape bool) {
		if len(parts) < 2 || len(parts) > cardMaxParts {
			return
		}
		for i := 0; i < len(parts); i++ {
			for j := i + 2; j <= len(parts); j++ {
				add(strings.Join(parts[i:j], "-"), shape)
			}
		}
	}
	for _, seg := range strings.FieldsFunc(text, isSegBreak) {
		var run []string // current run of adjacent content words
		flush := func() {
			if runs {
				for i := range run {
					for k := 2; k <= 3 && i+k <= len(run); k++ {
						add(strings.Join(run[i:i+k], "-"), false)
					}
				}
			}
			run = run[:0]
		}
		first := true
		for _, raw := range strings.Fields(seg) {
			end := strings.HasSuffix(raw, ".") || strings.HasSuffix(raw, ":")
			tok := strings.Trim(raw, ".:'\"/\\-_~^&$%@+")
			// paths and URLs: every segment is its own token
			for _, piece := range strings.Split(tok, "/") {
				if piece == "" || strings.Contains(piece, "@") {
					continue
				}
				rawParts := strings.FieldsFunc(piece, func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r)) })
				var parts []string
				for _, p := range rawParts {
					if np := normPart(p); np != "" {
						parts = append(parts, np)
					}
				}
				if len(parts) == 0 {
					flush()
					continue
				}
				compound := len(parts) >= 2
				if compound {
					addCompound(parts, true)
				}
				for i, p := range parts {
					if stopwords[p] {
						continue
					}
					orig := rawParts[min(i, len(rawParts)-1)]
					shape := compound || mixedAlnum(p) || (allCaps(piece) && len(piece) <= 8)
					if !first && !compound && len(piece) > 0 && unicode.IsUpper([]rune(piece)[0]) && !allCaps(orig) {
						shape = true // mid-sentence Capitalized: a proper noun
					}
					add(p, shape)
				}
				if compound || stopwords[parts[0]] {
					flush()
				} else {
					run = append(run, parts[0])
				}
			}
			first = false
			if end {
				flush()
				first = true
			}
		}
		flush()
	}
	return out
}

// IDCard is one task's identity card: term -> weight (idf x boosts).
type IDCard struct {
	W    map[string]float64
	Mass float64
}

// cardSource caches each task's extracted name and body terms.
type cardSource struct {
	name, body map[string]*cardTerm
}

// CardIndex holds cards for one corpus.
type CardIndex struct {
	N     int
	DF    map[string]int
	Cards map[string]*IDCard
}

func (ci *CardIndex) idf(t string) float64 {
	df := float64(ci.DF[t])
	return math.Log(1 + (float64(ci.N)-df+0.5)/(df+0.5))
}

// cardExtractCache memoizes per-task extraction by (slug, text identity).
var cardExtractCache struct {
	sync.Mutex
	m map[string]cardSource
}

func cardSourceOf(td TaskData, strip func(string) string) cardSource {
	var b strings.Builder
	b.WriteString(td.Brief)
	for _, u := range td.Updates {
		b.WriteString("\n")
		b.WriteString(u.Text)
	}
	body := b.String()
	key := fmt.Sprintf("%s\x00%d\x00%d\x00%t", td.Slug, len(td.Updates), len(body), strip != nil)
	cardExtractCache.Lock()
	if cardExtractCache.m == nil {
		cardExtractCache.m = map[string]cardSource{}
	}
	cs, ok := cardExtractCache.m[key]
	cardExtractCache.Unlock()
	if ok {
		return cs
	}
	if strip != nil {
		body = strip(body)
	}
	cs = cardSource{
		name: EntityTerms(td.Name+"\n"+td.Slug, true),
		body: EntityTerms(body, true),
	}
	cardExtractCache.Lock()
	cardExtractCache.m[key] = cs
	cardExtractCache.Unlock()
	return cs
}

// BuildCards builds a card of up to k terms for every task in corpus:
// all of its name/slug terms, then the body terms with the highest
// (1+log tf) x idf x shape boost among those with DF <= cardMaxDF.
// strip (optional) removes task slugs from body text first.
func BuildCards(corpus []TaskData, k int, strip func(string) string) *CardIndex {
	ci := &CardIndex{N: len(corpus), DF: map[string]int{}, Cards: map[string]*IDCard{}}
	srcs := make([]cardSource, len(corpus))
	for i, td := range corpus {
		srcs[i] = cardSourceOf(td, strip)
		seen := map[string]bool{}
		for t := range srcs[i].name {
			seen[t] = true
		}
		for t := range srcs[i].body {
			seen[t] = true
		}
		for t := range seen {
			ci.DF[t]++
		}
	}
	maxDF := int(math.Max(2, cardMaxDF*float64(ci.N)))
	for i, td := range corpus {
		cs := srcs[i]
		card := &IDCard{W: map[string]float64{}}
		for t, c := range cs.name {
			if ci.DF[t] > maxDF {
				continue
			}
			w := ci.idf(t) * cardNameBoost
			if c.shape {
				w *= cardShapeBoost
			}
			card.W[t] = w
		}
		type cand struct {
			t string
			s float64
		}
		var cands []cand
		for t, c := range cs.body {
			if _, ok := card.W[t]; ok || ci.DF[t] > maxDF {
				continue
			}
			s := (1 + math.Log(c.n)) * ci.idf(t)
			if c.shape {
				s *= cardShapeBoost
			}
			cands = append(cands, cand{t, s})
		}
		sort.Slice(cands, func(a, b int) bool {
			if cands[a].s != cands[b].s {
				return cands[a].s > cands[b].s
			}
			return cands[a].t < cands[b].t
		})
		for _, c := range cands {
			if len(card.W) >= k {
				break
			}
			w := ci.idf(c.t)
			if cs.body[c.t].shape {
				w *= cardShapeBoost
			}
			card.W[c.t] = w
		}
		for _, w := range card.W {
			card.Mass += w
		}
		ci.Cards[td.Slug] = card
	}
	return ci
}

// Card score normalizations.
const (
	CardNormQuery = "q" // matched weight / query's attainable weight (like pooled BM25)
	CardNormCard  = "c" // matched weight / card mass (share of the identity named)
)

// CardScore is the weighted overlap of query terms q with card c.
func (ci *CardIndex) CardScore(q map[string]*cardTerm, qmax float64, slug, norm string) float64 {
	c := ci.Cards[slug]
	if c == nil {
		return 0
	}
	var s float64
	if len(q) < len(c.W) {
		for t := range q {
			s += c.W[t]
		}
	} else {
		for t, w := range c.W {
			if q[t] != nil {
				s += w
			}
		}
	}
	switch norm {
	case CardNormCard:
		if c.Mass == 0 {
			return 0
		}
		return s / c.Mass
	default:
		if qmax == 0 {
			return 0
		}
		return s / qmax
	}
}

// queryMax bounds the card weight a query can match: the sum over its
// distinctive terms (DF <= cardMaxDF) of idf x the largest boost, so a
// query-normalized card score lies in [0,1].
func (ci *CardIndex) queryMax(q map[string]*cardTerm) float64 {
	maxDF := int(math.Max(2, cardMaxDF*float64(ci.N)))
	var s float64
	for t := range q {
		if df := ci.DF[t]; df > 0 && df <= maxDF {
			s += ci.idf(t) * cardShapeBoost * cardNameBoost
		}
	}
	return s
}

// CardScorer ranks by the identity-card score, alone or fused:
// BM25 + LSALambda x latent + Lambda x card (Alone: card only).
type CardScorer struct {
	Label     string
	K         int
	Norm      string
	Alone     bool
	Lambda    float64
	LSALambda float64 // > 0: add the LSA latent kNN (bm25+0.25lsa uses .25)
	Strip     func(string) string
	// Index / Space, when set, are the disk-cached card index and LSA space,
	// used verbatim so a live `flow match related` call rebuilds neither the
	// cards nor the SVD. The index must cover ps.Corpus.
	Index *CardIndex
	Space *LSASpace

	thr  float64
	lsa  *LSAScorer
	ci   *CardIndex
	qt   []map[string]*cardTerm
	qmax []float64
}

func (s *CardScorer) Name() string { return s.Label }

func (s *CardScorer) usesLSA() bool { return s.LSALambda > 0 }

// cardIndexCache shares cards between scorers with the same K on the
// same corpus.
var cardIndexCache struct {
	sync.Mutex
	keys []cardKey
	vals []*CardIndex
}

type cardKey struct {
	corpus *TaskData
	n, k   int
	strip  bool
}

func cardsFor(corpus []TaskData, k int, strip func(string) string) *CardIndex {
	key := cardKey{n: len(corpus), k: k, strip: strip != nil}
	if len(corpus) > 0 {
		key.corpus = &corpus[0]
	}
	cardIndexCache.Lock()
	for i, kk := range cardIndexCache.keys {
		if kk == key {
			ci := cardIndexCache.vals[i]
			cardIndexCache.Unlock()
			return ci
		}
	}
	cardIndexCache.Unlock()
	ci := BuildCards(corpus, k, strip)
	cardIndexCache.Lock()
	cardIndexCache.keys = append(cardIndexCache.keys, key)
	cardIndexCache.vals = append(cardIndexCache.vals, ci)
	if len(cardIndexCache.keys) > 8 {
		cardIndexCache.keys, cardIndexCache.vals = cardIndexCache.keys[1:], cardIndexCache.vals[1:]
	}
	cardIndexCache.Unlock()
	return ci
}

// Prepare builds the cards over ps.Corpus and extracts every query's
// entity terms (from QueryRec.text).
func (s *CardScorer) Prepare(ps *PairSet) {
	if s.Index != nil {
		s.ci = s.Index
	} else {
		s.ci = cardsFor(ps.Corpus, s.K, s.Strip)
	}
	s.qt = make([]map[string]*cardTerm, len(ps.Queries))
	s.qmax = make([]float64, len(ps.Queries))
	for qi, qr := range ps.Queries {
		s.qt[qi] = EntityTerms(qr.text, true)
		s.qmax[qi] = s.ci.queryMax(s.qt[qi])
	}
	if s.usesLSA() {
		if s.lsa == nil {
			s.lsa = &LSAScorer{P: DefaultLSAParams, M: 3, Mode: LSAFusion, Lambda: s.LSALambda}
		}
		s.lsa.Space = s.Space // nil = fit; set = cached space (no SVD)
		s.lsa.Prepare(ps)
	}
}

// BuildRelatedCards builds the identity-card index the shipped related
// pipeline fuses in (layer-2 card feature). Cached to disk and restored
// via RelatedOptions.Cards.
func BuildRelatedCards(tasks []TaskData) *CardIndex {
	var slugs []string
	for _, t := range tasks {
		slugs = append(slugs, t.Slug)
	}
	sm := NewSlugMatcher(slugs)
	strip := func(s string) string { out, _ := sm.Strip(s); return out }
	return BuildCards(tasks, RelatedCardK, strip)
}

// RelatedCardK is how many terms each task's identity card keeps in the
// shipped related pipeline (matches the layers-eval card scorer).
const RelatedCardK = 30

func (s *CardScorer) card(p Pair) float64 {
	return s.ci.CardScore(s.qt[p.Query], s.qmax[p.Query], p.Target, s.Norm)
}

func (s *CardScorer) Score(p Pair) float64 {
	c := s.card(p)
	if s.Alone {
		return c
	}
	base := p.A.BM25
	if s.usesLSA() {
		base = s.lsa.Score(p) // BM25 + LSALambda x latent
	}
	return base + s.Lambda*c
}

func (s *CardScorer) Fit(train []Pair) {
	scores := make([]float64, len(train))
	labels := make([]bool, len(train))
	for i, p := range train {
		scores[i], labels[i] = s.Score(p), p.Pos
	}
	s.thr = f1Threshold(scores, labels)
}

func (s *CardScorer) Belongs(p Pair) bool { return s.Score(p) >= s.thr }

// IDCardLineup is the identity-card sweep for the lookback eval: card
// alone (both normalizations), bm25 + λ·card and bm25 + 0.25·lsa + λ·card.
// The first sweep (card15, bm25+{.1..1}card30q, λ 1 on card-normalized
// fusions) is summarized in the kept/dropped ledger in docs/recall.md;
// dominated variants were dropped.
func IDCardLineup(strip func(string) string) []Scorer {
	out := []Scorer{
		&CardScorer{Label: "card30q", K: 30, Norm: CardNormQuery, Alone: true, Strip: strip},
		&CardScorer{Label: "card30c", K: 30, Norm: CardNormCard, Alone: true, Strip: strip},
		&CardScorer{Label: "card50q", K: 50, Norm: CardNormQuery, Alone: true, Strip: strip},
	}
	for _, l := range []float64{0.1, 0.25} {
		out = append(out, &CardScorer{Label: fmt.Sprintf("bm25+%gcard30c", l), K: 30, Norm: CardNormCard, Lambda: l, Strip: strip})
	}
	for _, l := range []float64{1, 2, 4} {
		out = append(out, &CardScorer{Label: fmt.Sprintf("bm25+0.25lsa+%gcard30q", l), K: 30, Norm: CardNormQuery, Lambda: l, LSALambda: 0.25, Strip: strip})
	}
	out = append(out, &CardScorer{Label: "bm25+0.25lsa+2card50q", K: 50, Norm: CardNormQuery, Lambda: 2, LSALambda: 0.25, Strip: strip})
	for _, l := range []float64{0.1, 0.25, 0.5} {
		out = append(out, &CardScorer{Label: fmt.Sprintf("bm25+0.25lsa+%gcard30c", l), K: 30, Norm: CardNormCard, Lambda: l, LSALambda: 0.25, Strip: strip})
	}
	return out
}
