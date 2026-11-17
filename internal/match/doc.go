// Package match is the ranking engine behind `flow recall`: given a query
// (a task's own name+brief, a free-text string, or a mid-session prompt) it
// returns the handful of past tasks most likely to be relevant, each with a
// calibrated confidence. Everything here is learned from the user's own flow
// history; nothing leaves the machine and no model ships beyond a few fitted
// coefficients.
//
// # The layered pipeline
//
// A recall call runs a five-layer pipeline. Layer 0 throws out tasks that
// cannot be the answer; layer 1 casts a wide net of candidates; layer 2
// re-ranks them with the one scorer whose gains survived the eval; layer 3
// decides how many of the top results are confident enough to show; layer 4
// renders them as one-liners the session model can read.
//
//	query (task brief | free text | prompt)
//	      │
//	      ▼
//	┌───────────────────────────────────────────────────────────────┐
//	│ L0  hard filters        drop self, own children/runs, the      │
//	│                         session's bound task + its children     │
//	│                         (unless --include-self), and (optional) │
//	│                         everything outside one customer/project │
//	├───────────────────────────────────────────────────────────────┤
//	│ L1  candidate net       every task that existed before the     │
//	│                         query (or all, with --all)             │
//	├───────────────────────────────────────────────────────────────┤
//	│ L2  re-rank             bm25 + 0.25·lsa + identity-card, the    │
//	│                         kept scorer (ShippedRelatedScorer)      │
//	├───────────────────────────────────────────────────────────────┤
//	│ L3  confidence cut      logistic p(relevant) per item; show     │
//	│                         while p ≥ MinP, at most K (RelatedConf, │
//	│                         or the per-user calibration)            │
//	├───────────────────────────────────────────────────────────────┤
//	│ L4  one-liners          [label] status slug — name + summary;   │
//	│                         a confident subset at p ≥ StrongP       │
//	└───────────────────────────────────────────────────────────────┘
//	      │
//	      ▼
//	strong / possible matches  (or "no confident matches")
//
// # The scorers
//
// The layer-2 ranker fuses three signals, each earned against the eval:
//
//   - BM25 over a task's pooled text (name, brief, update notes, cleaned
//     session prompts), the dominant lexical signal — see model.go.
//   - LSA: a truncated-SVD latent space fit over the same corpus, folded in
//     at weight 0.25 so co-occurring vocabulary bridges synonyms — lsa.go,
//     lsa_scorer.go (LSAFusion mode).
//   - The identity card: a task's most distinctive entity terms, fused as a
//     precision signal that lifts how often a real mention lands high —
//     idcard.go.
//
// # Confidence
//
// Ranking says which tasks are most related; it does not say whether any are
// related enough to surface. Layer 3 (confidence.go) runs a small logistic —
// p(relevant) over score/self, log rank, gap-to-next, log query length and
// query coverage (the IDF-weighted share of the query a candidate matches, so
// a top hit that shares only one strong word is not called strong on rank
// alone) — so the nudge can stay silent rather than guess. RelatedConf holds
// the shipped coefficients; `flow recall train --calibrate` refits them on the
// user's own links and persists the result (calibrate.go).
//
// # The eval
//
// RunLookback (lookback.go) is how every kept component earned its place: it
// holds out each task, ranks its related PAST tasks under forward-chaining
// folds, and reports MRR / recall@k with paired bootstrap CIs against the
// bm25 baseline. It is exposed as `flow recall eval` (dev-only) and is what
// `--calibrate` fits against. Components whose paired gain's CI straddled 0
// were dropped; see docs/recall.md for the full kept/dropped ledger.
//
// # Speed
//
// A live call must stay fast on a corpus that is always drifting, so the
// hydrated corpus, the BM25 term index and the fitted LSA space are cached to
// disk and keyed per task by a stat-based fingerprint: a call re-hydrates only
// the tasks that changed and folds them into the existing space (the app-side
// cache in internal/app/matchcache.go). A warm call is well under a second.
//
// # Memory
//
// The index is held compact without moving rankings: terms are interned to
// int32 ids against one shared vocabulary (so each distinct term string is
// stored once, not replicated across every task's maps), per-task field
// counts are float32 (exact for integer TFs), and the LSA basis/coordinates
// are float32 at K=128. A > 40%-DF auto-stoplist drops flow boilerplate from
// lexical scoring. DF=1 rare terms are deliberately kept — see docs/recall.md.
package match
