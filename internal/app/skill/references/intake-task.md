# Plain task intake: the full interview

The old §4.2 interview, kept for plain tasks. The default user path is
SKILL.md §4.2 "Start a driver" (one question at most).

**Use ONLY for:** an explicit "add a plain task" / "just track this, not a
driver", backlog capture ("remind me to X later", "park this"), purely
retrospective records, and automation. Everything else (the user states
work they want done) is SKILL.md §4.2 "Start a driver".

**The interview is the whole point** — you interview before saving.
NEVER solution during intake, NEVER fill blanks with guesses. If a
section is unclear, ask; if the user says "I don't know yet", write
"Open question: ..." in the brief and move on.

**Required sections (always asked, in this order):**

1. **Name** — one-sentence description (e.g. "Add OAuth login to the
   budgeting app").
2. **Slug** — short, memorable, ASCII. AskUserQuestion with 2–3
   candidates derived from the name (e.g. `oauth`, `auth-budget`,
   `oauth-budget`); "Other" lets them type a custom one. If they pick
   Other and leave it blank, omit `--slug`.
3. **Where?** — work_dir. Use the §6 recipe; pass `--mkdir` (AskUserQuestion
   "Yes, create it" / "No, I'll fix the path") if the path doesn't exist.
4. **Priority** — AskUserQuestion "High" / "Medium (Recommended)" / "Low",
   default Medium. Skip if already stated.
5. **Driver?** — AskUserQuestion "How should this task run?":
   "Driver (Recommended)" (owns it end to end, spawns `flow auto`
   children) / "Regular" (this session only). Recommend Driver for
   multi-part goals; on Driver add `--tag driver`. Never skip.

Also, before saving: **project attachment** — AskUserQuestion with one
option per existing project (label = slug, desc = name) plus "None
(floating task)"; skip if no projects exist.

**Optional sections (offered, can be deferred):** after the required
fields, AskUserQuestion:

> "Want to capture more detail now (Why, Done when, Out of scope, Open
> questions), or defer until you start the task?"
> - Detail now (recommended for tasks you'll start later)
> - Defer until you start the task

- **Detail now:** interview Why / Done when / Out of scope / Open
  questions and draft the full brief (template → references/templates.md).
- **Defer:** save a thin brief (template → references/templates.md). The
  §9 deferred-section prompt walks the missing sections at `flow do`
  time, when the user has more context.

**Confirmation:** show the drafted brief, then AskUserQuestion (header:
"Brief", "Save it" / "Revise"). Do NOT run `flow add task` until "Save
it"; on "Revise", ask what to change and re-confirm.

**After `flow add task` succeeds** it prints the slug and a stub
`brief.md` path. Overwrite the stub with your drafted content using
**Read once, then Edit (`replace_all: true`) or Write** — the harness
requires a prior Read before any mutation.

**Tag step — always ask, easy to skip.** Right after the brief saves,
before "Open now?", surface ONE tag question. The user skips with a
click; do NOT pre-skip on their behalf.

1. `flow list tags` for the existing vocabulary.
2. AskUserQuestion (header: "Tags?", `multiSelect: true`): if tags exist,
   offer the top 3 as `#<tag>` (desc "N tasks already have this tag") +
   "New tag(s)" (Other → comma-separated) + "Skip — no tags". If none
   exist, ask "Tag this task? (optional)" → "Yes, set tags" / "Skip — no
   tags"; on Yes, prompt for comma-separated values.
3. On any selection: `flow update task <slug> --tag <t1> --tag <t2> ...`.
   On "Skip": nothing, move on without dwelling.

The only legitimate pre-skip is when the user already said "just save
it" / "no more questions" this turn.

**Finally, "Open now?" — shape depends on binding.** Probe `flow show
task` (no arg): errors with `not bound to a task` → unbound (dispatch);
else already bound.

**Unbound — three options** (AskUserQuestion, header "Open now?"):
- **Yes, in a new tab** — §4.4 `flow do <slug>` (spawns a tab, flips to
  in-progress). Pick when work hasn't started.
- **Continue here (bind this session)** — run **`flow do --here <slug>`**
  now (binds the active harness session id, flips to in-progress, no tab).
  Pick when the work already began in this session — the common case when
  §4.14 triggered intake from the SessionStart intercept.
- **No, keep in backlog** — save and stop.

**Status follow-through:** neither Yes-path leaves the task in backlog
(both flip to in-progress). If the task is purely retrospective (records
something already complete here — "track the script I just wrote"),
immediately offer §4.7 closure after Continue-here (AskUserQuestion
"Mark done now?") so it moves backlog → in-progress → done — never leave
a retrospective record sitting in in-progress.

**Bound — two options ONLY** (AskUserQuestion "Yes, open it" / "No, keep
in backlog"). Do NOT offer "Continue here"/"Rebind"/any variant: a
session_id belongs to at most one task (partial unique index), and `flow
do --here` REJECTS a re-bind even with `--force` — offering it surfaces
an option the binary refuses. Intent is almost always "open in a separate
tab". On "Yes" → §4.4; on "No" → stop.

> **Different-tab hint.** "Continue here" only ever attaches the
> *current* dispatch session. To track work from a *different* open
> session, switch to that tab and run `flow do --here <slug>` there.
