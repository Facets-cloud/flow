-- base: v0.1.0-alpha.39
-- A DB that ran the pre-versioning #175 build (--model/--effort): its
-- per-open probes added tasks/owners model+effort, but user_version is
-- still 0. It must migrate through v1 then v2 with the values kept.
ALTER TABLE tasks ADD COLUMN model TEXT;
ALTER TABLE tasks ADD COLUMN effort TEXT;
ALTER TABLE owners ADD COLUMN model TEXT;
ALTER TABLE owners ADD COLUMN effort TEXT;
UPDATE tasks SET model = 'haiku', effort = 'low' WHERE slug = 't1';
UPDATE owners SET model = 'opus', effort = 'high' WHERE slug = 'keeper';
