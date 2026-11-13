# Report a flow bug (file a GitHub issue)

> Loaded on demand from the flow skill's resident core (SKILL.md). This file holds the full workflow; the core keeps a one-line trigger pointing here.

### 4.19 Report a flow bug

**Triggers:** the user says "report this", "file a bug", "open an issue",
"raise this with the flow team" about flow itself or a flow connector. Also
fires when YOU hit a clear flow bug (a `flow` command panics, contradicts
this skill, or corrupts state) — then OFFER to report it via
AskUserQuestion ("Report it" / "Not now"). Never draft or file unasked, and
never file without the human's explicit yes. A user mistake or a bug in the
user's own project is not a flow bug — don't offer.

**Where it goes:**

| Component | Repo |
|---|---|
| flow core (the `flow` binary, this skill, hooks, bus, owners, playbooks) | `Facets-cloud/flow` |
| connectors (`flow-mesh`, `flow-p2p`, other `flow connector` plugins) | `Facets-cloud/flow-connectors` |
| anything else (a harness bug, a third-party tool) | ask the human — don't guess |

**Recipe:**

1. **Gather** (run, don't guess):
   - `flow --version`
   - OS: `uname -sr`
   - harness + version: `claude --version` or `codex --version`
   - `flow connector list` (connector bugs, or if connectors may be involved)
   - the exact command that failed, expected vs actual behavior
   - a minimal repro (fewest steps from a clean state)
   - the relevant log lines only (e.g. from `tasks/<slug>/auto-runs/*.log`,
     the command's stderr) — not whole files
2. **Redact** before anything leaves the machine:
   - home paths → `~` (`/Users/<name>/…`, `/home/<name>/…`)
   - tokens, API keys, secrets, passwords, cookies, auth headers
   - customer, company, and person names (use `<customer>`, `<person>`)
   - invite links, peer/hub URLs, device ids, session ids if not needed
   - NEVER attach `flow.db`, KB files, briefs, or transcripts wholesale —
     quote only the minimal lines that show the bug, redacted.
3. **Draft** using this template exactly (write it to a temp file, e.g.
   `$(mktemp -t flow-issue).md`):

   ~~~markdown
   Title: <one line: component — symptom>

   ### What happened
   <actual behavior, the exact command>

   ### Expected
   <what should have happened>

   ### Repro
   1. ...
   2. ...

   ### Environment
   - flow: <flow --version>
   - OS: <uname -sr>
   - harness: <claude|codex> <version>
   - connectors: <flow connector list, or "none">

   ### Logs (redacted)
   ```
   <minimal redacted lines>
   ```
   ~~~

4. **Confirm.** Show the full draft (title + body + target repo) to the human,
   then AskUserQuestion (header "File issue?"): **"File it"** / **"Edit"** /
   **"Don't file"**. On Edit, apply their changes and re-ask. On Don't file,
   stop (you may keep the draft as an update note if they want). Never file
   without "File it". In a headless/auto run with no human, never file —
   put the draft in your update note and mention it in your report instead.
5. **File.** Run `gh auth status` first.
   - Authed: `gh issue create -R <repo> --title "<title>" --body-file <draft>`
     (body = the template minus the `Title:` line).
   - Not authed / `gh` missing: give the human the final draft and the URL to
     file it manually: `https://github.com/<repo>/issues/new`.
6. **Record.** Save the issue URL (or "drafted, filed manually") as a progress
   note on the current task: `tasks/<slug>/updates/YYYY-MM-DD-reported-<kebab>.md`
   (§4.5 format). If the bug blocks the task, also set `waiting_on` (§4.6).
