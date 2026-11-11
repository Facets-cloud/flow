# site/daily — "How flow is built, with flow"

A flow playbook (`site-daily`) runs this every morning. It reads the flow
database read-only, writes one HTML fragment and one JSON file, and refuses
to write anything that could leak.

```
~/.flow (read-only)                       site/
  flow.db  ──┐                             ├── built-with-flow.html   (fragment)
  tasks/<slug>/updates/*.md  ──┤  render.py ──┤
  tasks/<slug>/auto-runs/*.log ─┘   │          └── data/this-week.json
  playbooks/site-daily/denylist.txt ┘  (outside the repo, never committed)
```

## Files

| file | what |
|---|---|
| `render.py` | stdlib-only renderer. `python3 site/daily/render.py --flow-root ~/.flow --out site/` |
| `queries.sql` | every SQL query the page uses, one named block each, runnable with `sqlite3` |
| `test_render.py` | leak tests. `python3 -m unittest discover site/daily` (also runs in CI) |

Exit codes: `0` ok, `1` runtime error, `2` a leak check failed. On any
non-zero exit nothing is written and stderr carries only `check failed:
<name>`, never the matched text.

## What the page may contain

Only: task slugs, statuses, headless-run status, counts (tasks, children,
done, dead, messages between tasks, asks to the human, notes), top-level
file names under a task directory with counts, PR/issue numbers that
resolve to `Facets-cloud/flow`, and phrases from fixed tables in
`render.py` (`DEAD_PHRASES`, `STAT_LABELS`). No brief, note or message text.

## Privacy model (allowlist, not redaction)

1. **Scope.** `PROJECT_SLUGS = ("flow",)`. Only tasks whose `project_slug`
   is in that tuple are selected; children inherit the project so they are
   in; nothing else can be selected (`queries.sql: scoped_tasks`).
2. **Private tag.** Any task tagged `private` is dropped.
3. **Denylist.** `~/.flow/playbooks/site-daily/denylist.txt`, one token per
   line, case-insensitive, matched when not glued to a preceding
   letter/digit (so `sk-` fires on `sk-abc` but not on `task-list`). Any task
   whose slug hits a token is dropped. The run refuses to start if the file
   is missing or empty, and refuses to emit output that hits any token.
   Categories seeded: person and customer names, Slack channel/user ids,
   GCP project ids, email addresses, secret prefixes, the home directory,
   and every other `project_slug` in `flow.db`. It lives outside the repo.
4. **References.** `#NNNN` in notes and bus messages is kept only when the
   same line names `Facets-cloud/flow`, or `gh api` confirms the number
   exists in that repo (`--no-gh` disables the latter; results are cached
   outside the repo).
5. **Dead runs.** The newest `auto-runs/*.log` is matched against fixed
   regexes; the page shows the phrase from `DEAD_PHRASES`, never the log.
6. **Fail closed.** Output is rendered in memory, then scanned:
   `denylist_scan`, `absolute_path_scan`, `home_dir_scan`, `email_scan`,
   `secret_pattern_scan`, `json_html_parity`, `fixed_phrase_table`. Any
   failure writes nothing and exits 2. The previous output stays in place.

## Tests

`test_render.py` builds a fixture flow root with flow-project tasks (a
driver, children, a dead child with a login-failure log, a child with PR
numbers, an email and a home path in a note), a decoy task from another
project, a `private`-tagged flow task, a flow task whose slug carries a
denylist token, out-of-window and soft-deleted tasks, and bus traffic
including decoy asks. It asserts what appears, what never appears, that
JSON and HTML agree, that a missing or empty denylist aborts with no
output, and that a planted token aborts naming only the check. A final
test runs the renderer against the real `~/.flow` read-only and asserts the
output passes the denylist and carries no absolute paths; it is skipped
when `~/.flow/flow.db` is absent (CI).

## Wiring

`site/index.html` is not touched here. A later change includes the
fragment; `site/data/this-week.json` carries the same numbers for scripts.
