-- base: v0.1.0-alpha.6
-- Live DBs carry columns from unreleased branches (recall/match:
-- tasks.deleted_at, forked_from_slug) and a legacy parent_slug that
-- predates the session-invariant rebuild. All must survive it, with data.
ALTER TABLE tasks ADD COLUMN deleted_at TEXT;
ALTER TABLE tasks ADD COLUMN parent_slug TEXT REFERENCES tasks(slug);
ALTER TABLE tasks ADD COLUMN forked_from_slug TEXT;
UPDATE tasks SET deleted_at = '2026-01-02T00:00:00Z', parent_slug = 't1', forked_from_slug = 't1' WHERE slug = 't2';
