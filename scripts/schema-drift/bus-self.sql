-- base: v0.1.0-alpha.27
-- The bus's reserved human assignee was spelled 'self' before it became
-- 'user'; such DBs also predate seen_at. The baseline rewrites 'self' and
-- stamps existing human mail seen (old asks aren't resurfaced).
INSERT INTO bus_messages (id, created_at, kind, from_assignee, from_task_slug, to_assignee, to_task_slug, body)
  VALUES ('self0001', '2026-05-01T00:00:00Z', 'message', 't1', 't1', 'self', NULL, 'old ask to the human');
INSERT INTO bus_watches (watcher, watched, created_at) VALUES
  ('self', 'beta', '2026-05-01T00:00:00Z'),
  ('self/t1', 't2', '2026-05-01T00:00:00Z');
