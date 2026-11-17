package app

import (
	"encoding/gob"
	"encoding/json"
	"flow/internal/match"
	"os"
	"path/filepath"
)

// The lookback cache keeps `flow recall` fast across a corpus that is ALWAYS
// drifting: while flow is in use an active session's transcript keeps growing,
// so a whole-corpus signature would miss on every call and pay a full rebuild.
//
// Layout (all under $FLOW_ROOT/match/):
//
//	lookback-cache.gob   the BASE index: the slim task list (each with its
//	                     fingerprint), the term index (Model), the fitted LSA
//	                     space and the identity-card index. Written ONLY by a
//	                     full build / `flow recall train` — NEVER by a warm
//	                     recall call.
//	tasks/<slug>.gob     a per-task DELTA: one task that changed since the base
//	                     was written, as a vocab-independent snapshot that
//	                     re-applies to the base Model on load. A warm call
//	                     writes only the tasks that changed — the base is left
//	                     untouched, so a growing active-task transcript never
//	                     re-encodes the corpus.
//	manifest.json        tiny index of which tasks have a delta (slug ->
//	                     fingerprint) and which base tasks were removed.
//
// On load the base is decoded once and the deltas are folded in (term index
// via Model.ApplyEntry — exact BM25/DF; LSA via fold-in on the existing basis,
// NEVER a refit). Refits happen only in `flow recall train` (incl. the
// background run), which rewrites the base and clears the deltas.

// lookbackCacheVersion bumps whenever the on-disk BASE layout changes so a
// stale cache is transparently rebuilt instead of mis-decoded. v3: the LSA
// basis/coordinates/IDF narrowed to float32 (VFlat is now []float32).
const lookbackCacheVersion = 3

// manifestVersion bumps whenever the per-task delta / manifest layout changes.
const manifestVersion = 1

type lookbackCache struct {
	Version int
	Tasks   []match.TaskData // slimmed, each carrying its Fingerprint
	Space   *match.LSASpace
	Model   *match.Model
	Cards   *match.CardIndex // identity-card index the shipped ranker fuses in
	BasisN  int              // corpus size when the LSA basis was last fully fit
	Drift   int              // tasks folded into the basis since the last full fit
	VFlat   []float32
	VCols   int

	// baseSlugs is the set of slugs present in the BASE (before deltas). It is
	// unexported, so gob never persists it; loadBase fills it and the warm
	// path reads it to decide whether a changed task's LSA points already live
	// in the base (keep them) or must be folded from its delta (an added task).
	baseSlugs map[string]bool
}

// ---- paths ----

func lookbackCachePath() (string, error) {
	dir, err := recallDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lookback-cache.gob"), nil
}

func deltaDir() (string, error) {
	dir, err := recallDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tasks"), nil
}

func deltaPath(slug string) (string, error) {
	dir, err := deltaDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, slug+".gob"), nil
}

func manifestPath() (string, error) {
	dir, err := recallDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "manifest.json"), nil
}

// ---- base (index.gob) decode/encode ----

// loadBase decodes the base index, restores the flat LSA basis and re-indexes
// the derived structures. Returns nil on any absence / version mismatch /
// decode error (the caller then rebuilds). It does NOT apply deltas.
func loadBase() *lookbackCache {
	path, err := lookbackCachePath()
	if err != nil {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var c lookbackCache
	if err := gob.NewDecoder(f).Decode(&c); err != nil || c.Version != lookbackCacheVersion {
		return nil
	}
	if c.Space != nil {
		if c.Space.V == nil && c.VFlat != nil {
			c.Space.V = unflattenV(c.VFlat, c.VCols)
		}
		c.Space.Reindex()
	}
	if c.Model != nil {
		c.Model.Reindex() // rebuild the term->id vocab gob did not persist
	}
	c.baseSlugs = make(map[string]bool, len(c.Tasks))
	for _, t := range c.Tasks {
		c.baseSlugs[t.Slug] = true
	}
	return &c
}

// loadRawCache decodes the base and folds in every persisted per-task delta,
// yielding the current committed index. Returns nil when there is no usable
// base (the caller then does a cold full build).
func loadRawCache() *lookbackCache {
	base := loadBase()
	if base == nil {
		return nil
	}
	man := loadManifest()
	if man == nil || (len(man.Deltas) == 0 && len(man.Removed) == 0) {
		return base
	}
	applyDeltas(base, man)
	return base
}

// applyDeltas folds the manifest's per-task deltas and removals into base,
// in place. Stale or missing delta files are skipped (they are recomputed on
// the next call that re-detects the change).
func applyDeltas(base *lookbackCache, man *cacheManifest) {
	if base.Model == nil || base.Space == nil {
		return
	}
	bySlug := make(map[string]int, len(base.Tasks))
	for i, t := range base.Tasks {
		bySlug[t.Slug] = i
	}
	drop := make(map[string]bool, len(man.Removed)+len(man.Deltas))
	var foldPts []match.LSAPoint

	// Removals: a base task that no longer exists.
	for _, slug := range man.Removed {
		base.Model.Remove(slug)
		drop[slug] = true
		if i, ok := bySlug[slug]; ok {
			base.Tasks[i].Slug = "" // tombstone; compacted below
		}
	}

	// Deltas: changed or added tasks.
	for slug, fp := range man.Deltas {
		d := loadDelta(slug)
		if d == nil || d.Entry == nil || d.Entry.Fingerprint != fp {
			continue // missing/stale delta — leave base as-is, recomputed later
		}
		base.Model.ApplyEntry(d.Entry)
		if !d.InBase {
			// Added task: the base has no points for it; fold the delta's.
			drop[slug] = true // defensive: clear any stray points first
			foldPts = append(foldPts, d.Points...)
		}
		// InBase changed task: keep the base's LSA points (unchanged behavior).
		if i, ok := bySlug[slug]; ok {
			base.Tasks[i] = d.Slim
		} else {
			base.Tasks = append(base.Tasks, d.Slim)
		}
	}

	base.Space.ReplaceTasks(drop, foldPts)

	// Compact tombstoned slim entries.
	if len(man.Removed) > 0 {
		kept := base.Tasks[:0]
		for _, t := range base.Tasks {
			if t.Slug != "" {
				kept = append(kept, t)
			}
		}
		base.Tasks = kept
	}
	base.Drift = len(man.Deltas) + len(man.Removed)
}

// writeCache writes the BASE index atomically and clears any accumulated
// per-task deltas (the base now subsumes them). The latent basis is stored
// flat and per-point raw counts are dropped, both to shrink the file and speed
// decoding.
func writeCache(c *lookbackCache) error {
	path, err := lookbackCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	c.Version = lookbackCacheVersion
	if c.Space != nil {
		c.Space.Compact()
		if flat, cols, ok := flattenV(c.Space); ok {
			c.VFlat, c.VCols = flat, cols
			saved := c.Space.V
			c.Space.V = nil
			defer func() { c.Space.V = saved }()
		}
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(f).Encode(c); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	clearDeltas()
	return nil
}

// clearDeltas removes the per-task delta directory and the manifest, so a
// freshly-written base starts with no overrides. Best-effort.
func clearDeltas() {
	if dir, err := deltaDir(); err == nil {
		os.RemoveAll(dir)
	}
	if mp, err := manifestPath(); err == nil {
		os.Remove(mp)
	}
}

// ---- manifest + per-task delta files ----

// cacheManifest indexes the per-task deltas layered over the base.
type cacheManifest struct {
	Version int               `json:"version"`
	Deltas  map[string]string `json:"deltas"`  // slug -> fingerprint
	Removed []string          `json:"removed"` // base slugs no longer present
}

// taskDelta is one task's override of the base, written when it changes on a
// warm call. Entry is the vocab-independent term-index snapshot; Points are
// the LSA points to fold in on load, used ONLY for tasks not in the base
// (added tasks) — a changed base task keeps the base's points.
type taskDelta struct {
	Slim   match.TaskData
	Entry  *match.EntryData
	InBase bool
	Points []match.LSAPoint
}

func loadManifest() *cacheManifest {
	mp, err := manifestPath()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(mp)
	if err != nil {
		return nil
	}
	var m cacheManifest
	if json.Unmarshal(b, &m) != nil || m.Version != manifestVersion {
		return nil
	}
	if m.Deltas == nil {
		m.Deltas = map[string]string{}
	}
	return &m
}

func saveManifest(m *cacheManifest) error {
	mp, err := manifestPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(mp), 0o755); err != nil {
		return err
	}
	m.Version = manifestVersion
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := mp + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, mp)
}

func loadDelta(slug string) *taskDelta {
	p, err := deltaPath(slug)
	if err != nil {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	defer f.Close()
	var d taskDelta
	if gob.NewDecoder(f).Decode(&d) != nil {
		return nil
	}
	return &d
}

func writeDelta(d *taskDelta) error {
	p, err := deltaPath(d.Slim.Slug)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(f).Encode(d); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

func removeDelta(slug string) {
	if p, err := deltaPath(slug); err == nil {
		os.Remove(p)
	}
}

// ---- flat LSA basis helpers ----

// flattenV moves sp.V into one contiguous array (rows must be uniform width);
// false if V is empty or ragged (then V is gob-encoded as-is).
func flattenV(sp *match.LSASpace) ([]float32, int, bool) {
	if sp == nil || len(sp.V) == 0 {
		return nil, 0, false
	}
	cols := len(sp.V[0])
	if cols == 0 {
		return nil, 0, false
	}
	flat := make([]float32, 0, len(sp.V)*cols)
	for _, row := range sp.V {
		if len(row) != cols {
			return nil, 0, false
		}
		flat = append(flat, row...)
	}
	return flat, cols, true
}

// unflattenV re-slices a flat basis into rows sharing one backing array.
func unflattenV(flat []float32, cols int) [][]float32 {
	if cols <= 0 || len(flat) == 0 {
		return nil
	}
	rows := len(flat) / cols
	out := make([][]float32, rows)
	for i := 0; i < rows; i++ {
		out[i] = flat[i*cols : (i+1)*cols : (i+1)*cols]
	}
	return out
}

func slimAll(tasks []match.TaskData) []match.TaskData {
	out := make([]match.TaskData, len(tasks))
	for i, t := range tasks {
		out[i] = match.SlimForCache(t)
	}
	return out
}

// ---- builds ----

// fullBuildCache hydrates every task, builds the index and fits the LSA space
// from scratch, writes the base cache (clearing deltas) and returns the live
// artifacts.
func fullBuildCache(rows []matchRow) ([]match.TaskData, *match.LSASpace, *match.Model, *match.CardIndex, bool) {
	tasks := hydrateAll(rows)
	space := match.FitRelatedSpace(tasks)
	model := match.BuildRelatedIndex(tasks)
	cards := match.BuildRelatedCards(tasks)
	_ = writeCache(&lookbackCache{Tasks: slimAll(tasks), Space: space, Model: model, Cards: cards, BasisN: len(tasks)})
	return tasks, space, model, cards, false
}

// saveLookbackCache writes a full base cache from an already-hydrated corpus
// (used by `flow recall train`). Best-effort.
func saveLookbackCache(rows []matchRow, tasks []match.TaskData, space *match.LSASpace) error {
	if space == nil {
		space = match.FitRelatedSpace(tasks)
	}
	return writeCache(&lookbackCache{Tasks: slimAll(tasks), Space: space,
		Model: match.BuildRelatedIndex(tasks), Cards: match.BuildRelatedCards(tasks), BasisN: len(tasks)})
}

// hydrateCached returns the corpus, fitted LSA space and term index for rows,
// maintaining the per-task cache incrementally: only changed tasks are
// re-hydrated (never their transcript), and the base is never rewritten. The
// bool reports whether a usable base existed (false on a cold full build).
func hydrateCached(rows []matchRow) ([]match.TaskData, *match.LSASpace, *match.Model, *match.CardIndex, bool) {
	return hydrateCachedWith(rows, loadRawCache())
}

// hydrateCachedWith is hydrateCached with the cache already decoded (so a
// caller can decode it concurrently with matchScan — the two I/O phases then
// overlap).
func hydrateCachedWith(rows []matchRow, c *lookbackCache) ([]match.TaskData, *match.LSASpace, *match.Model, *match.CardIndex, bool) {
	if c == nil || c.Model == nil || c.Space == nil || c.Cards == nil {
		return fullBuildCache(rows)
	}
	cached := make(map[string]match.TaskData, len(c.Tasks))
	for _, t := range c.Tasks {
		cached[t.Slug] = t
	}
	present := make(map[string]bool, len(rows))
	var changed, added []matchRow
	for _, r := range rows {
		present[r.data.Slug] = true
		old, ok := cached[r.data.Slug]
		switch {
		case !ok:
			added = append(added, r)
		case old.Fingerprint != r.data.Fingerprint:
			changed = append(changed, r)
		}
	}
	var removed []string
	for slug := range cached {
		if !present[slug] {
			removed = append(removed, slug)
		}
	}
	churn := len(changed) + len(added) + len(removed)

	if churn == 0 { // pure hit: nothing re-hydrated
		tasks := make([]match.TaskData, 0, len(rows))
		for _, r := range rows {
			tasks = append(tasks, cached[r.data.Slug])
		}
		return tasks, c.Space, c.Model, c.Cards, true
	}

	// Load the manifest so this call's changes layer on top of earlier deltas.
	man := loadManifest()
	if man == nil {
		man = &cacheManifest{Version: manifestVersion, Deltas: map[string]string{}}
	}
	removedSet := make(map[string]bool, len(man.Removed))
	for _, s := range man.Removed {
		removedSet[s] = true
	}

	bySlug := make(map[string]match.TaskData, len(c.Tasks))
	for _, t := range c.Tasks {
		bySlug[t.Slug] = t
	}

	// Changed tasks: re-read only brief+notes (NEVER the transcript) and keep
	// the cached prompt index (a growing live transcript is almost always the
	// query task, excluded from its own candidates — a slightly stale prompt
	// index does not change which PAST tasks rank). Fresh prompts are folded
	// in at the next `flow recall train`.
	for _, r := range changed {
		lt := hydrateLight(r)
		lt.Prompts = cached[r.data.Slug].Prompts
		c.Model.UpsertKeepingPrompts(lt)
		slim := match.SlimForCache(lt)
		bySlug[r.data.Slug] = slim
		inBase := c.baseSlugs[r.data.Slug]
		if ed, ok := c.Model.ExportEntry(r.data.Slug); ok {
			var pts []match.LSAPoint
			if !inBase {
				pts = match.PointsOf(lt) // added-then-changed: base has no points
			}
			_ = writeDelta(&taskDelta{Slim: slim, Entry: ed, InBase: inBase, Points: pts})
			man.Deltas[r.data.Slug] = r.data.Fingerprint
		}
	}

	// Removed tasks: drop from the model and (base) space.
	drop := make(map[string]bool, len(removed))
	for _, s := range removed {
		drop[s] = true
		delete(bySlug, s)
		c.Model.Remove(s)
		removeDelta(s)
		delete(man.Deltas, s)
		if c.baseSlugs[s] && !removedSet[s] {
			man.Removed = append(man.Removed, s)
			removedSet[s] = true
		}
	}

	// Added tasks: full hydrate and fold in (a brand-new task's transcript is
	// tiny; the live GROWING task is "changed", not "added").
	var newPts []match.LSAPoint
	for _, r := range added {
		f := hydrate(r)
		c.Model.Upsert(f)
		pts := match.PointsOf(f)
		newPts = append(newPts, pts...)
		slim := match.SlimForCache(f)
		bySlug[r.data.Slug] = slim
		if ed, ok := c.Model.ExportEntry(r.data.Slug); ok {
			_ = writeDelta(&taskDelta{Slim: slim, Entry: ed, InBase: false, Points: pts})
			man.Deltas[r.data.Slug] = r.data.Fingerprint
		}
	}
	c.Space.ReplaceTasks(drop, newPts) // changed tasks keep their points; removed drop; added fold in

	_ = saveManifest(man)

	// Rebuild the slim list in corpus order.
	slim := make([]match.TaskData, 0, len(rows))
	for _, r := range rows {
		slim = append(slim, bySlug[r.data.Slug])
	}
	c.Tasks = slim
	return slim, c.Space, c.Model, c.Cards, true
}
