package app

import (
	"encoding/gob"
	"flow/internal/match"
	"os"
	"path/filepath"
)

// The lookback cache keeps `flow match related` — the live lookback nudge —
// fast across a corpus that is ALWAYS drifting: while flow is in use an
// active session's transcript keeps growing, so a whole-corpus signature
// would miss on every call and pay the full ~12 s rebuild (hydrate every
// transcript + fit the LSA SVD) each time. Instead the cache is keyed
// per task by that task's stat-based fingerprint: a call re-hydrates only
// the tasks that actually changed, folds their documents into the
// existing LSA space (no refit), and incrementally updates the term
// index. The expensive SVD is re-fit only when enough has drifted. With
// one task changed a warm call is well under a second.
//
// Stored under $FLOW_ROOT/match/lookback-cache.gob: the slimmed task list
// (layer-0 structure + layer-4 name/brief, each carrying its fingerprint),
// the pre-built term index and the fitted LSA space. The latent basis V
// (vocab × K float64) is stored flat (one contiguous array) because gob
// decodes nested slices slowly.

// lookbackCacheVersion bumps whenever the on-disk cache layout changes so a
// stale cache is transparently rebuilt instead of mis-decoded. v3: the LSA
// basis/coordinates/IDF narrowed to float32 (VFlat is now []float32).
const lookbackCacheVersion = 3

// lsaRefitFloor / lsaRefitShare set when the LSA basis is re-fit instead
// of folded into: when the tasks changed (this call plus accumulated
// drift) exceed max(floor, share × corpus).
const (
	lsaRefitFloor = 40
	lsaRefitShare = 0.2
)

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
}

func lookbackCachePath() (string, error) {
	dir, err := recallDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lookback-cache.gob"), nil
}

func maxChurn(n int) int {
	c := n / 5
	if c < lsaRefitFloor {
		c = lsaRefitFloor
	}
	return c
}

// loadRawCache decodes the cache, restores the flat basis into Space.V and
// re-indexes the space. Returns nil on any absence, version mismatch or
// decode error (the caller then rebuilds).
func loadRawCache() *lookbackCache {
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
	return &c
}

// writeCache persists the cache atomically. The latent basis is stored
// flat and the space's per-point raw counts are dropped (unused when
// scoring queries), both to shrink the file and speed decoding.
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
	return os.Rename(tmp, path)
}

// flattenV moves sp.V into one contiguous array (rows must be uniform
// width); false if V is empty or ragged (then V is gob-encoded as-is).
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

// fullBuildCache hydrates every task, builds the index and fits the LSA
// space from scratch, writes the cache and returns the live artifacts.
func fullBuildCache(rows []matchRow) ([]match.TaskData, *match.LSASpace, *match.Model, *match.CardIndex, bool) {
	tasks := hydrateAll(rows)
	space := match.FitRelatedSpace(tasks)
	model := match.BuildRelatedIndex(tasks)
	cards := match.BuildRelatedCards(tasks)
	_ = writeCache(&lookbackCache{Tasks: slimAll(tasks), Space: space, Model: model, Cards: cards, BasisN: len(tasks)})
	return tasks, space, model, cards, false
}

// saveLookbackCache writes a full cache from an already-hydrated corpus
// (used by `flow match train`). Best-effort.
func saveLookbackCache(rows []matchRow, tasks []match.TaskData, space *match.LSASpace) error {
	if space == nil {
		space = match.FitRelatedSpace(tasks)
	}
	return writeCache(&lookbackCache{Tasks: slimAll(tasks), Space: space,
		Model: match.BuildRelatedIndex(tasks), Cards: match.BuildRelatedCards(tasks), BasisN: len(tasks)})
}

// hydrateCached returns the corpus, fitted LSA space and term index for
// rows, maintaining the per-task cache incrementally: only changed tasks
// are re-hydrated, their documents are folded into the space, and the
// index is updated in place. The bool reports whether a usable cache
// existed (false on a cold full build).
func hydrateCached(rows []matchRow) ([]match.TaskData, *match.LSASpace, *match.Model, *match.CardIndex, bool) {
	return hydrateCachedWith(rows, loadRawCache())
}

// hydrateCachedWith is hydrateCached with the cache already decoded (so a
// caller can decode it concurrently with matchScan — the two ~300 ms I/O
// phases then overlap).
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

	if churn == 0 { // pure hit: nothing re-hydrated, cache untouched
		tasks := make([]match.TaskData, 0, len(rows))
		for _, r := range rows {
			tasks = append(tasks, cached[r.data.Slug])
		}
		return tasks, c.Space, c.Model, c.Cards, true
	}
	if churn > maxChurn(len(rows)) || c.Drift+churn > maxChurn(len(rows)) {
		return fullBuildCache(rows)
	}

	// Incremental update. Changed tasks: re-read only brief+notes (skip the
	// slow transcript) and keep their cached prompt index and LSA points.
	// Added tasks: full hydrate and fold in. Removed: drop.
	bySlug := make(map[string]match.TaskData, len(c.Tasks))
	for _, t := range c.Tasks {
		bySlug[t.Slug] = t
	}
	for _, r := range changed {
		lt := hydrateLight(r)
		// The flow transcript copy (internal/app/transcriptstore.go) lets us
		// pick up a changed task's NEW prompts cheaply — reading the copy's
		// jsonl, no transcript re-decode. This closes the gap where the cache
		// kept stale cached prompts until a full rebuild. Re-index fully so
		// the fresh prompts land. With no copy yet, keep the old cheap path:
		// reuse the cached pooled prompts and skip the costly re-read.
		if prompts, ok := promptsFromFlowCopy(r); ok {
			lt.Prompts = prompts
			c.Model.Upsert(lt)
		} else {
			lt.Prompts = cached[r.data.Slug].Prompts
			c.Model.UpsertKeepingPrompts(lt)
		}
		bySlug[r.data.Slug] = match.SlimForCache(lt)
	}
	drop := make(map[string]bool, len(removed))
	for _, s := range removed {
		drop[s] = true
		delete(bySlug, s)
		c.Model.Remove(s)
	}
	var newPts []match.LSAPoint
	for _, r := range added {
		f := hydrate(r)
		c.Model.Upsert(f)
		newPts = append(newPts, match.PointsOf(f)...)
		bySlug[r.data.Slug] = match.SlimForCache(f)
	}
	c.Space.ReplaceTasks(drop, newPts) // changed tasks keep their old points; only removed drop, added fold in

	// Rebuild the slim list in corpus order.
	slim := make([]match.TaskData, 0, len(rows))
	for _, r := range rows {
		slim = append(slim, bySlug[r.data.Slug])
	}
	c.Drift += churn
	c.Tasks = slim
	_ = writeCache(c)
	return slim, c.Space, c.Model, c.Cards, true
}
