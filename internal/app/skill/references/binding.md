# Bind an in-flight session to a task

> Loaded on demand from the flow skill's resident core (SKILL.md). This file holds the full workflow; the core keeps a one-line trigger pointing here.

### 4.16 Bind an in-flight Claude session to a task

**Triggers:** "bind this session to <task>", "track this session
under <task>", "attach this conversation to <task>", "this session is
for <task>". Also fires when the user manually creates a flow task
while already deep in an ad-hoc Claude session and wants future
`flow do <slug>` to resume *this* conversation rather than start a
new one. The §4.2 "Continue here" option is the most common entry
point.

**Recipe:** if the target task already exists (or you just created
it via §4.2), run:

```
flow do --here <slug>
```

`flow do --here` reads the current session's UUID from
`$CLAUDE_CODE_SESSION_ID` (Claude Code injects this into every
session), validates it, and writes it to `tasks.session_id`. Side
effects: status flips backlog → in-progress (the session-id
invariant requires it). No terminal spawn happens; the binding is
the only mutation.

**Safety properties enforced by the binary** (you don't have to
police these):

- Refuses if `$CLAUDE_CODE_SESSION_ID` is unset (not a Claude Code
  session) or not a v4 UUID.
- Refuses if **THIS session** is already bound to a different task.
  `--force` does NOT override this — session_id uniqueness is
  structural. The user must release the prior binding first or
  open the target in a new tab via `flow do <slug>`.
- Refuses if **the target task** already has a different
  session_id bound. `--force` overrides this case (and only this
  case), but the user has been told it orphans the target's
  prior session.
- Refuses if **this Claude session's spawn directory ≠
  `task.work_dir`**. Flow maintains the invariant *any task with
  a session_id has work_dir equal to the cwd that session was
  created at* — that's what makes `flow do <slug>` resumes find
  the harness's on-disk transcript (the path is keyed by encoded
  cwd). The check is honest: the binary stats the expected
  transcript file on disk, not `os.Getwd()`. **`cd <work_dir>
  && flow do --here` does NOT bypass this** — the chained `cd`
  only changes the flow subprocess's cwd, not where the actual
  Claude session jsonl was written. `--force` does NOT override
  this gate — see the sub-recipe below.
- No-op (idempotent) if the target is already bound to this same
  session.
- Refuses if the target is `done`. Reopen via
  `flow update task <slug> --status in-progress` first; the
  prior session_id is preserved across done, so `--here` becomes
  unnecessary after reopen.

**Cwd-mismatch sub-recipe:**

When `flow do --here <slug>` exits non-zero with stderr
containing "the harness transcript isn't where work_dir says",
the binary stat'd the expected transcript path on disk and it
didn't exist — meaning **the Claude session you're currently in
was started in a directory other than `task.work_dir`.** This is
a fact about the running Claude process, not about your current
Bash shell cwd. Whatever directory you `cd` into now does NOT
change where the session jsonl was written. The binary's check
detects this and refuses; trying to retry from a different cwd
won't help.

Surface the choice via `AskUserQuestion` — do **not** auto-retry,
and **do not run `cd <work_dir> && flow do --here <slug>`** in
the hope of getting past the check. That doesn't work and will
hit the same refusal:

```
AskUserQuestion({
  questions: [{
    question: "This Claude session was started in a different directory than task `<slug>`'s work_dir (<work_dir>). The session's on-disk transcript isn't where future `flow do` resumes would look. What do you want to do?",
    header: "Cwd mismatch",
    options: [
      { label: "Open in a new tab (Recommended)",  description: "Run `flow do <slug>` to spawn a fresh Claude session at the task's work_dir. THIS session stays unbound and keeps doing whatever it was doing. Best when you weren't really working on the task here." },
      { label: "Point work_dir at THIS session's directory", description: "If you actually want this Claude session to own the task, update the task's work_dir to wherever this Claude was started (you'll need to figure that out — try the cwd of the shell that launched Claude). I'll then retry --here. Allowed even when a session is already bound, but the new work_dir must match where the harness transcript actually lives." }
    ],
    multiSelect: false
  }]
})
```

On "Open in a new tab" → run `flow do <slug>` (no `--here`).
THIS session stays unbound; the task gets its own new session
at work_dir.

On "Point work_dir at THIS session's directory" → ask the user
what cwd Claude was started in (they typed `claude` from
somewhere — that's the path). Then run
`flow update task <slug> --work-dir <real-cwd>`, then
`flow do --here <slug>`. The work_dir update will succeed only
if the harness transcript is actually at the named path
(binary validates by stat). If the user picks the wrong path,
the update errors with "harness transcript isn't there"; ask
them to try again.

**Why "cd then retry" is NOT listed as an option:** the binary
verifies the on-disk transcript path, which is fixed at Claude
session start. Bash subprocess cwd is irrelevant. `cd
<work_dir> && flow do --here` will produce the same refusal as
`flow do --here` from anywhere else — it's not a workaround.

**Anti-patterns:**

- **Do not invent or guess the session UUID.** The env var is the
  only authoritative source.
- **Do not bind without confirming the task slug.** If multiple
  tasks could plausibly own this conversation, AskUserQuestion to
  pick.
- **Do not try `cd <work_dir> && flow do --here` to bypass a
  cwd-mismatch refusal.** The check stats the on-disk transcript
  path; chained-cd doesn't change where the jsonl is. The binary
  will refuse identically.
- **Do not try `--force` to bypass a cwd-mismatch refusal.**
  `--force` overrides "already bound to a different session"
  but NOT the cwd invariant. Pick one of the two sub-recipe
  remedies instead.
- **Do not run `--here` from a different tab to attach a session
  in another tab.** The env var is per-process; `--here` always
  attaches the *current* session. To attach a session in another
  tab, switch to that tab and run `flow do --here` there.
