# Playbooks — add, run, first-run capture

> Loaded on demand from the flow skill's resident core (SKILL.md). This file holds the full workflow; the core keeps a one-line trigger pointing here.

### 4.12 Add a playbook

**Triggers:** "add a playbook", "create a playbook for X",
"track this as a playbook", "this is something I'll re-run".

**The interview is the whole point** (same philosophy as §4.2 task intake — you interview, then write down what the user said; you do NOT solution during intake).

**Sections to ask, ONE AT A TIME, in this order:**

1. **What?** One sentence describing what each run does.
2. **Why?** Why this playbook exists and what value it produces.
3. **Where?** Work_dir for runs (use §6 recipe).
4. **Each run does** — concrete steps every invocation performs. Bullet
   form. Replaces "Done when" from task intake.
5. **Out of scope?** Non-goals. Optional.
6. **Signals to watch for** — observable conditions that should change
   the run's behavior or trigger an escalation. Replaces "Open
   questions" — playbooks have long lifespans so prospective signals
   matter more than open questions.

**Then before calling `flow add playbook`:**

- Suggest 2-3 slug candidates via `AskUserQuestion` (header:
  "Pick a slug", one option per candidate plus "Other" for custom).
- Project attachment via `AskUserQuestion` (header: "Attach to?",
  one option per existing project plus "None (floating playbook)").
  Skip the question if there are no projects.
- `--mkdir` if work_dir doesn't exist — use `AskUserQuestion`
  (header: "Create dir?", options: "Yes, create it" / "No, fix the
  path") same as §6 step 6.

**Draft the brief, show to the user**, then use `AskUserQuestion`
(header: "Brief", options: "Save it" / "Revise") to confirm. Do not
run `flow add playbook` until the user picks "Save it". Then run it
and overwrite the stub `brief.md` with the full content (Read once,
then Edit/Write — same pattern as §5.2). Use the playbook brief
template from §7.

After save, use `AskUserQuestion` (header: "Run it now?", options:
"Run it now" / "Just save the definition") to offer the first run.
On "Run it now", proceed to §4.13. On "Just save the definition",
stop.

### 4.13 Run a playbook

**Triggers — any of these means "run `flow run playbook <slug>`":**
- "run the X playbook" / "trigger X" / "fire the X playbook"
- "fire the X agent" (legacy term users may use — playbook is the canonical name)
- "start a run of X" / "kick off X"
- "run X autonomously / unattended / in the background" → the `--auto`
  run mode below
- A bare `flow run playbook X` typed as command

**Recipe:**

1. Probe binding with `flow show task` (no arg). If it errors with
   `not bound to a task`, this is a dispatch (unbound) session — the
   in-session bind option is available. If it resolves a task, this
   session is already bound; only the new-tab path is available.

2. Use AskUserQuestion to pick the run mode. **Unbound session — four
   options** (header: "Run mode?"):

   - **In this session (bind here)** — runs `flow run playbook <slug> --here`.
     The new playbook-run task is created, the brief is snapshotted, and THIS
     conversation is bound to it. No new tab. Pick when the user wants the
     playbook to execute in the current chat (preserves transcript, no tab
     switch). Implicitly skips the `--dangerously-skip-permissions` question
     — there's no claude spawn to forward it to.
   - **New tab — regular** — runs `flow run playbook <slug>`. Spawns a
     fresh tab with tool-approval prompts.
   - **New tab — skip permissions** — runs `flow run playbook <slug>
     --dangerously-skip-permissions`. Spawns a fresh tab without
     approval prompts (faster).
   - **Autonomous (background)** — runs `flow run playbook <slug>
     --auto`. Headless, no tab, no human: the run does the work and
     self-completes via `flow done`. Implies skip-permissions; cannot
     combine with `--here`. Pick this for unattended / scheduled runs.
     Returns immediately — report that the run was launched and stop
     (don't poll it). Run status surfaces as `auto_run: running |
     completed | dead` on the run-task (see §4.4's autonomous-mode notes).

   **Bound session — three options** (header: "Run mode?", same options
   minus "In this session"): the binary refuses `--here` when the
   current session is already bound (session_id uniqueness invariant;
   `--force` does not override). Offering it would surface an option
   the binary will reject — bad UX. (Autonomous still applies — it
   spawns its own detached run, independent of the current binding.)

3. Run the chosen invocation. Skip the session-mode question entirely
   if the user already specified a mode in their request (e.g. "fire X
   in this session", "run X in a new tab", "run X autonomously").

4. The command creates a kind=playbook_run task and snapshots the brief
   in both paths. On the new-tab path it spawns a terminal tab that
   boots the flow skill. On the `--here` path it binds the current
   session — your job is to invoke the flow skill yourself and proceed
   against the snapshotted brief at `~/.flow/tasks/<run-slug>/brief.md`.

**Anti-pattern (per §8):** never auto-fire. Manual trigger only. Even if
the user mentions a playbook name in passing, do not run it without an
explicit verb ("run", "trigger", "fire", "start").

#### Persisting in-run adjustments back to the playbook

A playbook run executes against a **frozen snapshot** of the playbook's
`brief.md`. Sometimes during a run the user adjusts the procedure —
"let's always do X here", "change the approach for step 3", "this step
should also check Y". When that happens, the run-time session has two
sources of truth diverging:

- The run's `brief.md` snapshot — what THIS run is executing against
- The playbook's live `brief.md` — what FUTURE runs will inherit

If the user's adjustment is meant to apply only to this run, do nothing
extra. But if it's a procedural improvement worth keeping, the live
playbook brief should be updated — otherwise next week's run forgets
the lesson.

**Trigger this prompt when:** the user makes a non-trivial procedural
change during a run — adds a step, changes the approach for a step,
adds a signal to watch for, narrows or expands scope. Tiny tactical
tweaks ("skip step 4 today, the system is offline") don't count;
durable changes do.

**Recipe — use AskUserQuestion:**

```
AskUserQuestion({
  questions: [{
    question: "Persist this change to the playbook so future runs include it?",
    header: "Persist?",
    options: [
      { label: "Persist to playbook",  description: "Edit playbooks/<slug>/brief.md so future runs inherit the change" },
      { label: "Just this run",         description: "Apply to this run only; future runs continue with the existing playbook" },
      { label: "Both — persist + note", description: "Edit the live playbook AND log the rationale in playbooks/<slug>/updates/" }
    ],
    multiSelect: false
  }]
})
```

**Important rules:**

- **Never edit the run-task's own `brief.md`** to change future behavior.
  That's a frozen snapshot — editing it has no effect on future runs and
  obscures what the run actually executed against.
- **The live playbook brief lives at `~/.flow/playbooks/<slug>/brief.md`.**
  Edit that file directly when persisting.
- **The "Both" option** is the right answer when the change is worth
  capturing AND its rationale is non-obvious from the diff alone — the
  update note explains *why*, the brief edit captures *what*.
- **Do not auto-persist without asking.** Even a clear improvement may
  be deliberately scoped to this run by the user.

#### First-run capture (special case)

The **first run** of a playbook is where the actual procedure
crystallizes. The brief was written aspirationally; concrete commands,
scripts, decision rules, and edge cases get discovered for the first
time. Without active capture, all that learning evaporates when the run
closes.

**Detection:** the bootstrap prompt sets a banner — "⚡ THIS IS THE
FIRST RUN OF THIS PLAYBOOK ⚡" — when the run-task is the only
non-archived `kind=playbook_run` row for its `playbook_slug`. Treat
that as your signal.

**Behavior on first run — be more proactive than usual:**

1. **Scripts and commands.** When you write a script, settle on a
   concrete command, or develop a snippet that wasn't in the brief,
   pause and AskUserQuestion *immediately*:

   ```
   AskUserQuestion({
     questions: [{
       question: "Capture this <script|command|decision> back to the playbook?",
       header: "Capture?",
       options: [
         { label: "Add to playbook brief",  description: "Append/edit the relevant section of playbooks/<slug>/brief.md — future runs see it inline" },
         { label: "Save as sidecar file",   description: "Write to playbooks/<slug>/<topic>.md (e.g., decision-tree.md, sample-script.md). Surfaced under other: for on-demand load" },
         { label: "Just this run",          description: "Apply locally; don't change the playbook (rare for first run)" }
       ],
       multiSelect: false
     }]
   })
   ```

2. **Edge cases / signals.** When the user hits a condition the brief
   didn't anticipate, AskUserQuestion whether to add it to the "Signals
   to watch for" section of the live brief.

3. **End-of-run capture sweep.** Before `flow done`, AskUserQuestion:

   > "Capture anything from this run back to the playbook before closing?"
   > - Yes — walk me through what to capture
   > - No, close out as-is

   On "walk me through": list the candidate captures (scripts produced,
   decisions made, edge cases hit, commands actually used). Offer each
   one via AskUserQuestion individually so the user can opt in
   per-item.

**Sidecar files vs brief edits:**

- **Brief edits** are for *procedural* changes — additions to "Each run
  does", new "Signals to watch for", clarified scope. Inline content
  that every future run benefits from seeing during bootstrap.
- **Sidecar files** (`playbooks/<slug>/<topic>.md`) are for *artifacts*
  — scripts, decision trees, sample outputs, reference tables. Things
  that future runs may or may not need; they're surfaced under `other:`
  in `flow show playbook` and loaded on-demand by the run session.

**Capture-back is a primary deliverable of the first run.** Not an
afterthought. After the first run, the playbook should be
substantially more concrete than it started.
