-- site/daily/queries.sql — every query the "How flow is built, with flow" page uses.
-- One query per block, named. render.py loads this file and substitutes
-- {projects} with one "?" per allowed project slug (render.PROJECT_SLUGS).
-- Everything else (time window, denylist, private tag, counting) happens in
-- render.py so it can be unit-tested without a live database.
--
-- Run any block yourself:
--   sqlite3 ~/.flow/flow.db "<paste the SELECT with 'flow' in place of {projects}>"

-- name: scoped_tasks
-- Every task that belongs to an allowed project. Children inherit project_slug,
-- so they are selected here too; nothing outside the allowlist can be selected.
SELECT slug, status, kind, parent_slug, auto_run_status, created_at, status_changed_at, archived_at
FROM tasks
WHERE project_slug IN ({projects})
  AND deleted_at IS NULL;

-- name: private_tags
-- Tasks tagged `private` are dropped from the page regardless of project.
SELECT task_slug
FROM task_tags
WHERE lower(tag) = 'private';

-- name: scoped_messages
-- Bus traffic that touches an allowed-project task on either end. render.py
-- then keeps only: (a) messages whose BOTH endpoints are allowed-project tasks
-- that survived the privacy filters -> "messages between tasks"; and
-- (b) messages from such a task to the human (to_assignee = 'user',
-- no to_task_slug, fyi = 0) -> "asks to the human". Bodies are read only to
-- extract #NNNN references; body text never reaches the output.
SELECT id, created_at, from_task_slug, to_assignee, to_task_slug, body, fyi
FROM bus_messages
WHERE from_task_slug IN (SELECT slug FROM tasks WHERE project_slug IN ({projects}) AND deleted_at IS NULL)
   OR to_task_slug   IN (SELECT slug FROM tasks WHERE project_slug IN ({projects}) AND deleted_at IS NULL);
