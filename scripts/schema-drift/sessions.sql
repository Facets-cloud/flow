-- base: v0.1.0-alpha.6
-- Pre-invariant, pre-unique-index data the baseline must repair: two tasks
-- sharing a session_id (newest updated_at keeps it) and an in-progress
-- task with no session (demoted to backlog).
INSERT INTO tasks (slug, name, status, priority, work_dir, session_id, session_started, created_at, updated_at) VALUES
  ('drift-dup-new', 'Dup new', 'in-progress', 'medium', '/tmp', '11111111-1111-4111-8111-111111111111', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-03-01T00:00:00Z'),
  ('drift-dup-old', 'Dup old', 'done', 'medium', '/tmp', '11111111-1111-4111-8111-111111111111', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-02-01T00:00:00Z'),
  ('drift-nosession', 'No session', 'in-progress', 'medium', '/tmp', NULL, NULL, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
