# The work_dir question — rules

> Loaded on demand from the flow skill's resident core (SKILL.md).

## 6. The `work_dir` question — rules

When you're about to ask the user "where does this task live?", run
these steps BEFORE asking, so the question is informed:

1. **Run `flow workdir list`.** Fuzzy-match the task name against
   registered nicknames and paths. If you get an obvious match (e.g.
   task "Add OAuth to budgeting-app" and a registered workdir named
   `budgeting-app`), propose that path via `AskUserQuestion` (header:
   "Use this path?", options: "Yes, use `<path>`" / "Pick a different
   path"). On "Pick a different path", continue to step 2.
2. **If no local match, check GitHub via `gh`.** Run `gh repo list
   --limit 50 --json name,owner,description`. If any repo name or
   description plausibly matches the task, present the top 3 via
   `AskUserQuestion` (header: "Which repo?") with one option per
   candidate (label = `<repo-name>`, description = repo description)
   plus a "None of these — use a path instead" option. If the user
   picks a repo, offer (via `AskUserQuestion`, header: "Clone it?",
   options: "Yes, clone to `~/code/<name>`" / "No, I'll handle it")
   to run `gh repo clone <owner>/<repo> ~/code/<name>` and, after
   clone, run `flow workdir add ~/code/<name>` so next time it's a
   local match.
3. **If `gh` isn't authenticated** (command errors with an auth
   message), fall back gracefully via `AskUserQuestion` (header:
   "GitHub unreachable", options: "Give me a path" / "Make it
   floating"). On "Give me a path", prompt the user for an absolute
   path (this single text input is fine — there are no enumerable
   options). On "Make it floating", skip work_dir entirely.
4. **If the user wants a floating task** (no repo), skip the question
   entirely and let `flow add task` auto-create
   `~/.flow/tasks/<slug>/workspace/`.
5. **Never guess a path.** Don't invent `~/code/foo` because the task
   name sounds like "foo". Always confirm via `AskUserQuestion`.
6. **If the path doesn't exist**, use `AskUserQuestion` (header:
   "Create dir?", options: "Yes, create it" / "No, fix the path")
   to ask whether to pass `--mkdir`. On "Yes", append `--mkdir` to
   the `flow add task` invocation. On "No", loop back to ask for a
   corrected path.
