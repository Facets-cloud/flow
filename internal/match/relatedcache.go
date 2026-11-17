package match

// This file supports the per-task recall cache (internal/app/matchcache.go):
// a vocab-independent snapshot of one task's indexed Model entry, so a small
// per-task file can be written when only that task changes and re-applied to
// a base Model on load — without rewriting the whole corpus index.
//
// The snapshot is string-keyed (not interned against a particular Model's
// vocabulary), so applying it to any Model re-interns its terms and maintains
// DF / DocLen / TotalLen exactly, identical to Upsert.

// EntryData is a vocab-independent copy of a TaskEntry. All term maps are
// string-keyed; ApplyEntry re-interns them against the target Model.
type EntryData struct {
	Slug           string
	Name           string
	Status         string
	Archived       bool
	CreatedAt      string
	DoneAt         string
	SessionStarted string
	Fingerprint    string
	NumPrompts     int
	Fields         [numFields]map[string]float64
	PromptTF       []map[string]float64
}

// ExportEntry returns a vocab-independent snapshot of the task's entry, or
// (nil,false) if the task is not indexed. Term ids are resolved back to
// strings via the model's one vocabulary, so the result is self-contained.
func (m *Model) ExportEntry(slug string) (*EntryData, bool) {
	e := m.Tasks[slug]
	if e == nil {
		return nil, false
	}
	ed := &EntryData{
		Slug: e.Slug, Name: e.Name, Status: e.Status, Archived: e.Archived,
		CreatedAt: e.CreatedAt, DoneAt: e.DoneAt, SessionStarted: e.SessionStarted,
		Fingerprint: e.Fingerprint, NumPrompts: e.NumPrompts,
	}
	for f := range e.Fields {
		fm := make(map[string]float64, len(e.Fields[f]))
		for id, c := range e.Fields[f] {
			fm[m.Terms[id]] = float64(c)
		}
		ed.Fields[f] = fm
	}
	for _, pc := range e.PromptTF {
		pm := make(map[string]float64, len(pc))
		for id, c := range pc {
			pm[m.Terms[id]] = float64(c)
		}
		ed.PromptTF = append(ed.PromptTF, pm)
	}
	return ed, true
}

// ApplyEntry inserts (or replaces) the snapshot's task in the model, interning
// its terms against the model's vocabulary and maintaining DF, DocLen and
// TotalLen exactly — the same bookkeeping as Upsert, but from a pre-tokenized,
// vocab-independent snapshot (so no source text or re-tokenization is needed).
func (m *Model) ApplyEntry(ed *EntryData) {
	if ed == nil {
		return
	}
	m.Remove(ed.Slug)
	e := &TaskEntry{
		Slug: ed.Slug, Name: ed.Name, Status: ed.Status, Archived: ed.Archived,
		CreatedAt: ed.CreatedAt, DoneAt: ed.DoneAt, SessionStarted: ed.SessionStarted,
		Fingerprint: ed.Fingerprint, NumPrompts: ed.NumPrompts,
	}
	for f := range ed.Fields {
		ic := make(idCounts, len(ed.Fields[f]))
		for t, c := range ed.Fields[f] {
			ic[m.intern(t)] = float32(c)
		}
		e.Fields[f] = ic
	}
	for _, pc := range ed.PromptTF {
		ic := make(idCounts, len(pc))
		for t, c := range pc {
			ic[m.intern(t)] = float32(c)
		}
		e.PromptTF = append(e.PromptTF, ic)
	}
	for t := range e.termIDs() {
		m.DF[t]++
	}
	e.DocLen = m.docLen(e)
	m.TotalLen += e.DocLen
	m.Tasks[ed.Slug] = e
	m.rev++
	m.vecs, m.knnVecs, m.termSets, m.bm25tf = nil, nil, nil, nil
}
