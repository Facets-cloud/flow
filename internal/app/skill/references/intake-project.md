# Add a project (intake)

> Loaded on demand from the flow skill's resident core (SKILL.md). This file holds the full workflow; the core keeps a one-line trigger pointing here.

### 4.3 Add a project

**Triggers:** "add a project", "new project", "track this initiative".

Similar to §5.2 but shorter. Sections: **What / Why / Where / Scope**.
No "done when" (projects are ongoing containers, not completable units).
Confirm the `work_dir`. Draft. Show. Wait for "save it". Run `flow add
project`, then update the stub `brief.md` with the drafted content
(Read once, then Edit/Write — same pattern as §5.2).

Do not offer `flow do` on the project itself — you `do` tasks, not
projects.

**MANDATORY follow-up: create at least one task under the project.**
A project with zero tasks is a dead container — the user will forget
why they made it. Immediately after `flow add project` succeeds:

1. Say: "Project created. A project needs at least one task to be
   useful — what's the first concrete thing you want to do under
   <project-slug>?" (Use the project's actual slug.)
2. When the user answers, enter the task-intake workflow (§5.2)
   with `--project <slug>` pre-filled. Interview for What / Why /
   Where / Done when / Out of scope / Open questions as usual.
3. If the user says "I don't know yet" or "just create the project
   for now", DO NOT create a placeholder task and DO NOT silently
   drop it. Instead, explicitly tell them: "OK, no task for now —
   just tell me when you're ready to add one and I'll set it up."
   Do not surface the underlying `flow` commands.
4. If the user describes several tasks at once, create them all via
   sequential §5.2 interviews. Don't try to batch-extract; one
   interview per task.
5. Only after the first task exists (or the user has explicitly
   declined), use `AskUserQuestion` (header: "Open it now?", options:
   "Yes, open it" / "No, keep in backlog") to offer
   `flow do <first-task>`. If "Yes", proceed to §4.4. If "No", stop.

The rule is about pushing the user one step further than
`flow add project` — project creation is not a complete action on
its own, it's the start of a two-or-more-step workflow.
