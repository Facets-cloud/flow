PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE projects (
    slug          TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','done')),
    priority      TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('high','medium','low')),
    work_dir      TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    archived_at   TEXT
);
INSERT INTO "projects" VALUES('alpha','Alpha','active','high','/tmp/wd/alpha','2026-11-19T07:02:26Z','2026-11-19T07:02:26Z',NULL);
CREATE TABLE playbooks (
    slug          TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    project_slug  TEXT REFERENCES projects(slug),
    work_dir      TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    archived_at   TEXT
);
INSERT INTO "playbooks" VALUES('weekly','Weekly',NULL,'/tmp/wd/alpha','2026-11-19T07:02:26Z','2026-11-19T07:02:26Z',NULL);
CREATE TABLE tasks (
    slug                  TEXT PRIMARY KEY,
    name                  TEXT NOT NULL,
    project_slug          TEXT REFERENCES projects(slug),
    status                TEXT NOT NULL DEFAULT 'backlog' CHECK (status IN ('backlog','in-progress','done')),
    kind                  TEXT NOT NULL DEFAULT 'regular' CHECK (kind IN ('regular','playbook_run')),
    playbook_slug         TEXT REFERENCES playbooks(slug),
    priority              TEXT NOT NULL DEFAULT 'medium' CHECK (priority IN ('high','medium','low')),
    work_dir              TEXT NOT NULL,
    waiting_on            TEXT,
    due_date              TEXT,
    assignee              TEXT,
    status_changed_at     TEXT,
    session_id            TEXT,
    session_started       TEXT,
    session_last_resumed  TEXT,
    harness               TEXT,
    auto_run_status       TEXT,
    auto_run_pid          INTEGER,
    auto_run_started      TEXT,
    auto_run_finished     TEXT,
    auto_run_log          TEXT,
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL,
    archived_at           TEXT,
    CHECK (status = 'backlog' OR session_id IS NOT NULL)
);
INSERT INTO "tasks" VALUES('t1','Task one','alpha','backlog','regular',NULL,'medium','/tmp/wd/alpha','bob',NULL,NULL,'2026-11-19T07:02:26Z',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,'2026-11-19T07:02:26Z','2026-11-19T07:02:26Z',NULL);
INSERT INTO "tasks" VALUES('t2','Floating',NULL,'backlog','regular',NULL,'medium','/tmp/wd/float',NULL,'2026-12-01',NULL,'2026-11-19T07:02:26Z',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,'2026-11-19T07:02:26Z','2026-11-19T07:02:26Z','2026-11-19T07:02:26Z');
CREATE TABLE workdirs (
    path          TEXT PRIMARY KEY,
    name          TEXT,
    description   TEXT,
    git_remote    TEXT,
    last_used_at  TEXT,
    created_at    TEXT NOT NULL
);
INSERT INTO "workdirs" VALUES('/tmp/wd/alpha','alpha',NULL,NULL,'2026-11-19T07:02:26Z','2026-11-19T07:02:26Z');
INSERT INTO "workdirs" VALUES('/tmp/wd/float',NULL,NULL,NULL,'2026-11-19T07:02:26Z','2026-11-19T07:02:26Z');
CREATE TABLE task_tags (
    task_slug   TEXT NOT NULL REFERENCES tasks(slug) ON DELETE CASCADE,
    tag         TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    PRIMARY KEY (task_slug, tag)
);
CREATE INDEX idx_tasks_project    ON tasks(project_slug);
CREATE INDEX idx_tasks_status     ON tasks(status);
CREATE INDEX idx_tasks_updated_at ON tasks(updated_at);
CREATE INDEX idx_task_tags_tag    ON task_tags(tag);
CREATE INDEX idx_tasks_kind          ON tasks(kind);
CREATE INDEX idx_tasks_playbook_slug ON tasks(playbook_slug);
CREATE INDEX idx_playbooks_project   ON playbooks(project_slug);
CREATE UNIQUE INDEX idx_tasks_session_id ON tasks(session_id) WHERE session_id IS NOT NULL;
COMMIT;
