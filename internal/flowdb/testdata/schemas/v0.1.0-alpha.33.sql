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
    archived_at   TEXT,
    terminal_group TEXT
);
INSERT INTO "projects" VALUES('alpha','Alpha','active','high','/tmp/wd/alpha','2026-11-19T07:18:56Z','2026-11-19T07:18:56Z',NULL,NULL);
CREATE TABLE playbooks (
    slug          TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    project_slug  TEXT REFERENCES projects(slug),
    work_dir      TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    archived_at   TEXT
);
INSERT INTO "playbooks" VALUES('weekly','Weekly',NULL,'/tmp/wd/alpha','2026-11-19T07:18:56Z','2026-11-19T07:18:56Z',NULL);
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
    parent_slug           TEXT REFERENCES tasks(slug),
    created_at            TEXT NOT NULL,
    updated_at            TEXT NOT NULL,
    archived_at           TEXT,
    CHECK (status = 'backlog' OR session_id IS NOT NULL)
);
INSERT INTO "tasks" VALUES('t1','Task one','alpha','in-progress','regular',NULL,'high','/tmp/wd/alpha','bob',NULL,NULL,'2026-11-19T07:18:56Z','aaaaaaaa-1111-4222-8333-444444444444','2026-11-01T10:00:00Z',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,'2026-11-19T07:18:56Z','2026-11-19T07:18:56Z',NULL);
INSERT INTO "tasks" VALUES('t2','Floating',NULL,'backlog','regular',NULL,'medium','/tmp/wd/float',NULL,'2026-12-01',NULL,'2026-11-19T07:18:56Z',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,'2026-11-19T07:18:56Z','2026-11-19T07:18:56Z','2026-11-19T07:18:56Z');
CREATE TABLE workdirs (
    path          TEXT PRIMARY KEY,
    name          TEXT,
    description   TEXT,
    git_remote    TEXT,
    last_used_at  TEXT,
    created_at    TEXT NOT NULL
);
INSERT INTO "workdirs" VALUES('/tmp/wd/alpha','alpha',NULL,NULL,'2026-11-19T07:18:56Z','2026-11-19T07:18:56Z');
INSERT INTO "workdirs" VALUES('/tmp/wd/float',NULL,NULL,NULL,'2026-11-19T07:18:56Z','2026-11-19T07:18:56Z');
CREATE TABLE task_tags (
    task_slug   TEXT NOT NULL REFERENCES tasks(slug) ON DELETE CASCADE,
    tag         TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    PRIMARY KEY (task_slug, tag)
);
INSERT INTO "task_tags" VALUES('t1','fixture','2026-11-19T07:18:56Z');
CREATE TABLE owners (
    slug              TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    work_dir          TEXT NOT NULL,
    project_slug      TEXT REFERENCES projects(slug),
    status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','retired')),
    every             TEXT NOT NULL,
    next_wake_at      TEXT,
    last_tick_at      TEXT,
    last_tick_status  TEXT,
    tick_pid          INTEGER,
    tick_started      TEXT,
    harness           TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL,
    archived_at       TEXT
);
INSERT INTO "owners" VALUES('keeper','Keeper','/tmp/wd/alpha',NULL,'active','1h',NULL,NULL,NULL,NULL,NULL,NULL,'2026-11-19T07:18:56Z','2026-11-19T07:18:56Z',NULL);
CREATE TABLE bus_messages (
    id                 TEXT PRIMARY KEY,
    created_at         TEXT NOT NULL,
    kind               TEXT NOT NULL CHECK (kind IN ('message','broadcast')),
    from_assignee      TEXT NOT NULL,
    from_task_slug     TEXT,
    sender_session_id  TEXT,
    to_assignee        TEXT NOT NULL,
    to_task_slug       TEXT,
    body               TEXT NOT NULL,
    urgent             INTEGER NOT NULL DEFAULT 0,
    status             TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','delivered','acked')),
    delivered_at       TEXT,
    acked_at           TEXT,
    waited_s           REAL,
    acked_by           TEXT,
    reply_to           TEXT,
    seen_at            TEXT,
    consumed_by_identity TEXT,
    consumed_by_session  TEXT
);
INSERT INTO "bus_messages" VALUES('32e7ee6b','2026-11-19T07:18:56Z','message','user','t1','aaaaaaaa-1111-4222-8333-444444444444','user',NULL,'hello from fixture',0,'pending',NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL);
CREATE TABLE bus_watches (
    watcher     TEXT NOT NULL,
    watched     TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    PRIMARY KEY (watcher, watched)
);
INSERT INTO "bus_watches" VALUES('user/t1','alpha','2026-11-19T07:18:56Z');
CREATE TABLE bus_nudges (
    task_slug  TEXT PRIMARY KEY,
    nudged_at  TEXT NOT NULL,
    attempts   INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE bus_human_surfaced (
    session_id  TEXT NOT NULL,
    message_id  TEXT NOT NULL,
    surfaced_at TEXT NOT NULL,
    PRIMARY KEY (session_id, message_id)
);
CREATE TABLE bus_surfaced (
    task_slug   TEXT NOT NULL,
    message_id  TEXT NOT NULL,
    PRIMARY KEY (task_slug, message_id)
);
CREATE INDEX idx_tasks_project    ON tasks(project_slug);
CREATE INDEX idx_tasks_status     ON tasks(status);
CREATE INDEX idx_tasks_updated_at ON tasks(updated_at);
CREATE INDEX idx_task_tags_tag    ON task_tags(tag);
CREATE INDEX idx_tasks_kind          ON tasks(kind);
CREATE INDEX idx_tasks_playbook_slug ON tasks(playbook_slug);
CREATE INDEX idx_playbooks_project   ON playbooks(project_slug);
CREATE UNIQUE INDEX idx_tasks_session_id ON tasks(session_id) WHERE session_id IS NOT NULL;
CREATE INDEX idx_bus_messages_inbox
    ON bus_messages(to_assignee, to_task_slug, status);
CREATE INDEX idx_bus_messages_sender
    ON bus_messages(sender_session_id, status);
COMMIT;
