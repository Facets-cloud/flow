**Dry run.** This PR is written by the `site-daily` flow playbook from the
flow project's own tasks in `~/.flow`. It is a draft on purpose: for the
first seven runs the playbook only pushes here and never merges.

What changed: `site/built-with-flow.html` (an HTML fragment, not yet wired
into `site/index.html`), `site/data/this-week.json` (same numbers as data),
and `site/daily/queries.sql` (the queries, so readers can run them).

Leak checks that ran before anything was written, in order:

1. `denylist_present` — the out-of-repo denylist exists and is non-empty
2. scope — only `project_slug IN ('flow')`, `private`-tagged tasks dropped, denylisted slugs dropped
3. `denylist_scan` — no denylist token in the HTML or JSON
4. `absolute_path_scan` — no `/Users/`, `/home/`, `~/` and friends
5. `home_dir_scan` — the running user's home directory string is absent
6. `email_scan` — no email addresses
7. `secret_pattern_scan` — no Slack/GitHub/OpenAI/AWS token shapes
8. `json_html_parity` — every counter in the HTML equals the JSON
9. `fixed_phrase_table` — every "why it died" phrase comes from `DEAD_PHRASES`
10. `python3 -m unittest discover site/daily` — 17 tests including a read-only run on the real `~/.flow`

The page carries slugs, statuses, counts, `#refs` that resolve to
`Facets-cloud/flow`, and templated phrases. No brief, note, message or log
text. See `site/daily/README.md` for the model.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
