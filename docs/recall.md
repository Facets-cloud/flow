# `flow recall` — related past work from your own history

`flow recall` surfaces the handful of past tasks most likely to be relevant to
what you are doing now. It is a nudge for a Claude/Codex session: before you
design or implement something, it points you at the past work that already
touched it. Everything is learned from your own flow history — the task
briefs, update notes and cleaned session transcripts in `~/.flow`. Nothing
leaves the machine.

```
flow recall                              related past work for the BOUND task (reverse lookup)
flow recall <task-slug>                  ... for that task (arg exactly matches an existing slug)
flow recall "<free text>"                free-text lookback (arg is not a slug)
flow recall [<slug>] --prompt "<text>"   in-session: a prompt, with the task brief as context
flow recall train [--calibrate]          rebuild the index; --calibrate refits the confidence model
flow recall eval                         held-out evaluation of the ranker (dev-only)
```

Flags on the lookup forms: `--k N` (how many to show), `--min P` (confidence
cut), `--project X` (restrict to a project/prefix), `--include-children`,
`--include-self` (keep the bound task itself — see L0 below), `--all` (rank
every task, not only earlier ones), `--text` (force free-text mode on an
argument that happens to match a slug), `--json`.

## Slug vs. text

An argument that **exactly** matches an existing task slug is treated as a
task (rank its related work). Anything else is free text. A multi-word
argument is always text (slugs have no spaces). `train` and `eval` are
reserved subcommands. `--text` forces text mode even for a one-word argument
that collides with a slug.

## How it works — the layered pipeline

A recall call runs five layers (`internal/match`, with the package doc in
`internal/match/doc.go`):

| Layer | What | Where |
|---|---|---|
| **L0** hard filters | drop the query itself, its own children / playbook runs, the session's **bound task** (and its children — a bound session never recalls the task it is sitting in, in any mode; `--include-self` keeps it), and — with `--project` — everything outside one project or slug prefix | `related.go`, `prompt.go` |
| **L1** candidate net | every task that existed before the query (all tasks with `--all`) | `related.go` |
| **L2** re-rank | `bm25 + 0.25·lsa + card` — the kept ranker (`ShippedRelatedScorer`) | `lsa_scorer.go`, `idcard.go` |
| **L3** confidence cut | logistic `p(relevant)`; show while `p ≥ MinP`, at most `K` | `confidence.go`, `related.go` |
| **L4** one-liners | `[label] status slug — name` + a one-line summary; a confident subset at `p ≥ StrongP` | `related.go` |

### Why each L2 signal is there (and what was dropped)

Every component was measured by the lookback eval (`flow recall eval`), which
holds out each task, ranks its related *past* tasks under forward-chaining
folds, and reports MRR / recall@k with **paired bootstrap 95% CIs** against a
plain-BM25 baseline. The fail-fast rule: keep a component only if its paired
gain's CI excludes 0. The numbers below are from the layers and prompt
lookback experiments (seed 1), measured on a personal history of ~500 tasks.

**Kept:**

- **BM25** — the dominant lexical signal; the baseline every addition must beat.
- **LSA at 0.25** (`bm25+0.25lsa`, LSAFusion) — a truncated-SVD latent space
  fit over the same corpus, folded in so co-occurring vocabulary bridges
  synonyms. Candidate-generation recall@50 **+0.041 [+0.028, +0.057]** over
  BM25; re-rank mention MRR **+0.038 [+0.018, +0.060]**. CI clear of 0.
- **The identity card** — each task's most distinctive entity terms, fused as
  a precision signal. Mention recall@10 **+0.017 [+0.002, +0.038]**, MRR flat
  — it helps a real *mention* land in the top 10 without hurting anything. (As
  a standalone candidate generator it *loses* — mention recall −0.090 — so it
  is a fused feature, not a retriever.)

**Dropped (gain's CI straddled 0, or a regression):**

- **RRF of BM25+LSA** — regresses mention MRR **−0.038 [−0.061, −0.016]**.
- **The agreement-count feature** — no incremental effect.
- **The structure / recency prior** — the most promising near-miss (recency
  earned the largest learned weight) but ΔMRR **+0.020 [−0.009, +0.052]** still
  straddles 0; the project prior is noise.
- **The continuation verdict** (an 11-feature logistic + a cosine "dominance
  guard") and the **source-aware field scorers** — these lost to the simpler
  `bm25+0.25lsa+card` ranker on every ranking metric.
- **Query coverage as a layer-2 soft-AND** (`score × coverage^α`, damping a
  candidate that matches only a sliver of the query's IDF mass) — it regressed
  ranking: on the lookback eval `bm25+0.25lsa×cov` lost paired MRR on
  non-project **−0.027 [−0.046, −0.009]** and parent **−0.024 [−0.044,
  −0.008]** vs the plain fused ranker. Coverage damps exactly the low-overlap
  identifier matches the card is there to rescue, so it was kept out of the
  ranking and used only as a confidence feature (below).
- **Synonym / alias query expansion** — a built-in generic alias list
  (k8s↔kubernetes, db↔database, …) plus per-user acronym mining ("AWS Partner
  Network (APN)" learned on an explicit pattern), expanding each query term
  with its equivalents. On the lookback eval it was flat on MRR (all +0.004,
  non-project +0.001, mention −0.002, parent +0.011 — all within noise) and
  cost a small, consistent recall@10 drift (all −0.005, parent −0.007,
  family −0.015). Built-in-only measured identical to built-in+mined, i.e.
  mining added nothing against mention/parent truth. No component cleared the
  CI-positive bar, so expansion was not shipped.

All of the dropped code lived on the `exp/*` branches and has been removed from
the shipped tree (it stays in git history). The shipped `internal/match` is
~7k lines, down from ~13k.

## Confidence and calibration

Ranking says which tasks are most related; confidence (layer 3) says whether
any are related *enough* to show. A small logistic scores each top-10 item on
five features — `score/self`, `log rank`, `gap to next / self`, `log query
tokens`, and **query coverage** (the IDF-weighted share of the query's
informative terms the candidate matches) — and the nudge shows items while
`p ≥ 0.20` (`RelatedMinP`), at most 2 (`RelatedCap`); a `p ≥ 0.60` subset is
labelled **strong**, the rest **possible**. Query coverage is what lets
confidence tell "#1 because it covers the whole query" from "#1 because it
shares one strong word": a top hit that matches a single term out of four
covers ~0.25 of the query's IDF mass and no longer earns **strong** on rank
alone. On the eval the nudge shows ~1.5 tasks when it shows any, is right of
the order of two-thirds of the time, helps on a majority of new tasks and
stays silent on the rest — so it can answer "no confident matches" rather than
guess.

The shipped coefficients (`RelatedConf`) were fit on a personal history.
`flow recall train --calibrate` refits the same logistic on **your** links,
using the shipped ranker and strict truth (mention / parent / family /
playbook), and persists the result to `$FLOW_ROOT/match/recall-calib.json`,
which recall then uses in place of the default. Calibration is **refused** (a
friendly message, the default kept) below `MinCalibQueries` (50) truth-bearing
queries — too little signal to trust a refit over the shipped prior.
Free-text queries use `RelatedConf`; in-session `--prompt` mode uses its own
`RelatedPromptConf` (a prompt carries the brief as context, a different
operating point), and is not overridden by `--calibrate`.

## Abstaining on thin queries

Before ranking a free-text or prompt query, recall decides whether it is
distinctive enough to be worth ranking at all — otherwise it reports "no
confident matches (prompt too generic)" rather than guessing. A query clears
the bar if it names an identifier, or if its in-corpus terms' **summed IDF**
reaches the IDF of a single term at the rare-DF threshold (`abstainInfoT`).
This is a smooth informativeness test: earlier code bucketed terms by hard DF
cliffs (one term under 5%, or two under 30%) and so abstained on real queries
that straddled a boundary — "redis data migration" (redis just over 5% DF,
migration just over 30%) counted as a single mid term and was dropped, while
"redis data copy" ranked. Summed IDF has no cliff, so both now surface the
same past redis work. Words the user has never touched (DF 0) carry no mass and
are admitted only through the identifier shape.

## Auto-refresh and the cache

A live corpus is always drifting — an active session's transcript keeps
growing — so the index is cached to disk and keyed **per task** by a
stat-based fingerprint (`$FLOW_ROOT/match/lookback-cache.gob`). Every recall
call re-hydrates only the tasks that actually changed, folds them into the
existing LSA space (no SVD refit), and updates the term index in place; the
expensive SVD is re-fit only when enough has drifted. The first-ever call
builds the whole index (printing `building index…` on stderr); `flow recall
train` forces a full rebuild. Warm calls are well under a second (cold is
~12 s).

## Memory

Recall and train set a soft memory limit via `runtime/debug.SetMemoryLimit`
(default 400 MiB), so a cold build over a large history does not balloon. Set
the `GOMEMLIMIT` environment variable to override it — when it is present the
Go runtime's value is left alone.

### Compact index

The resident index is dominated by the term index (string-keyed `float64`
maps over ~240k distinct terms, many of them DF=1 bigrams) plus the LSA
tables. It is held compact without moving rankings:

- **Interned term vocabulary.** Terms are mapped to `int32` ids against one
  shared vocabulary (`Model.Terms` + the rebuilt `vocab`); every per-task
  field map and the DF table key by id, so each distinct term string is
  stored once instead of being replicated across every task's maps. Per-task
  field counts are stored as `float32` (exact for integer TFs). Queries stay
  in string space and resolve to ids at scoring time, so the ranking math is
  unchanged (`model.go`).
- **float32 LSA.** The latent basis `V` (vocab×K), the per-point coordinates
  `Coord` and the IDF table are `float32`; accumulation still runs in
  `float64`. A truncated SVD carries nothing like `float64` precision, so this
  is free (`lsa.go`).
- **Fewer latent dims.** `DefaultLSAParams.K` is 128 (was 200): `V` and
  `Coord` shrink ~36%. The lookback eval moves only within noise.
- **Auto-stoplist.** Terms present in > 40% of tasks (flow boilerplate —
  task/goal/brief/done/…) are dropped from BM25, cosine and the
  coverage/informativeness math (`StopDF`, guarded to histories of ≥25
  tasks). IDF already discounts them; excluding them is neutral on the
  shipped ranker and lifts the pure-lexical baselines on the eval.

Measured on a frozen ~500-task history (seed 1): warm RSS ≈ 330 → 220 MB,
cold-build peak ≈ 570 → 490 MB, on-disk cache 58 → 30 MB, latency unchanged.
Example-query top-5 and scores are **identical** across the two pure-storage
changes (interning + float32); the LSA-dim and stoplist changes keep every
top-1 and move the eval only within its CI.

**Not pruned: DF=1 terms.** Rare unigrams (identifiers) and DF=1 bigrams
(phrases) are the bulk of the vocabulary and the floor on this footprint, but
they are **kept**. They would measure *eval*-neutral — but only as an artifact
of the held-out design: a DF=1 term's sole owner is the task that is excluded
from its own candidates, so such terms can never move a cross-task eval rank.
They remain the strongest signal for live free-text phrase/identifier matching,
which the eval structurally under-measures, so pruning them would be neutral on
paper and lossy in practice.

## Evaluating and tuning

- **The eval harness is behind a build tag.** The held-out benchmark and its
  report are **not** compiled into the shipped `flow` binary — only the recall
  path (and `flow recall train --calibrate`, which needs the calibration fit,
  not the report) ships by default. Build the eval binary with the
  `recalleval` tag to run it:

  ```bash
  go build -tags recalleval -o flow-eval .
  ./flow-eval recall eval [--seed N] [--boot N] [--exclude <globs>]
  # also: go test -tags recalleval ./internal/match/   # eval-only tests
  ```

  In a default build, `flow recall eval` prints a short "not built; rebuild
  with -tags recalleval" note and exits non-zero. The eval machinery lives in
  `internal/match/lookback*.go` + `confidence_report.go` (all tagged
  `//go:build recalleval`); the shared query/ranking primitives it and the
  shipped path both use are in `internal/match/lookback_shared.go` (untagged).

- `flow recall eval [--seed N] [--boot N] [--exclude <globs>]` (eval build)
  runs the held-out lookback evaluation and prints MRR / recall@k with CIs. It
  is dev-only and is what `--calibrate` fits against.
- Tuning knobs live in `internal/match`: the LSA weight (`LSAFusion` Lambda,
  shipped 0.25), the identity-card depth (`RelatedCardK`, 30), the confidence
  threshold/cap (`RelatedMinP` 0.20, `RelatedCap` 2), and the LSA parameters
  (`DefaultLSAParams`). Change one, re-run `flow recall eval`, and keep it only
  if the paired gain's CI clears 0.

## Known limits

- **Thin prompt data.** The eval samples *in-task* prompts, which structurally
  favour the brief; the real value of `--prompt` (off-task / free-text
  queries) is under-measured. `RelatedPromptConf` is provisional.
- **Identifier mismatch.** Lexical + latent retrieval can miss past work that
  uses different words for the same thing and shares no distinctive
  identifier; the zero-overlap floor then (correctly) drops it rather than
  padding a guess.
- **Small n.** Calibration and the eval are over a few hundred queries on a few
  hundred tasks — read the CIs, not the point estimates.
- **Memory / cold start.** The first call on a large history pays the full
  build; subsequent calls are cheap.

## Output schema

`--json` emits a stable object: `task`, `mode`, `scorer`, `candidates`,
`min_p`, optional `project`/`abstain`, and `matches[]` with `slug`, `status`,
`name`, `summary`, `label`, `confidence` (the calibrated `p`), and `score`
(the raw layer-2 score).
