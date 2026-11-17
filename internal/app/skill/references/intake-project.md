# Add a project (intake)

> Loaded on demand from the flow skill's resident core (SKILL.md). This file holds the full workflow; the core keeps a one-line trigger pointing here.

### 4.3 Add a project

**Triggers:** "add a project", "new project", "track this initiative".

Similar to the plain-task interview (references/intake-task.md) but shorter. Sections: **What / Why / Where / Scope**.
No "done when" (projects are ongoing containers, not completable units).
Confirm the `work_dir`. Draft. Show. Wait for "save it". Run `flow add
project`, then update the stub `brief.md` with the drafted content
(Read once, then Edit/Write — same pattern as task intake).

Do not offer `flow do` on the project itself — you `do` tasks, not
projects.

**MANDATORY follow-up: start the first driver under the project.**
(Skip this when the project was created by SKILL.md §4.2 Start a driver:
that driver task already is its first task.)
A project with zero tasks is a dead container — the user will forget
why they made it. Immediately after `flow add project` succeeds:

1. Ask: "Project created — what do you want to achieve first?" (one
   question; skip it if the user already said).
2. When the user answers, run SKILL.md §4.2 Start a driver with the
   project pre-set (its work_dir comes from the project): the answer is
   the driver's Goal, no further interview.
3. If the user says "I don't know yet" or "just create the project
   for now", DO NOT create a placeholder task and DO NOT silently
   drop it. Instead, explicitly tell them: "OK, nothing to start yet —
   just tell me when you're ready and I'll set it up."
   Do not surface the underlying `flow` commands.
4. Several goals at once → one driver per goal (or one driver whose
   children split the work, if they serve one outcome). Explicit plain
   tasks / backlog items use references/intake-task.md instead.
5. Binding follows §4.2 step 5: unbound session → bind here and
   continue; already bound → open the driver in a new tab.

The rule is about pushing the user one step further than
`flow add project` — project creation is not a complete action on
its own, it's the start of a two-or-more-step workflow.
