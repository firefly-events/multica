CREATE TABLE IF NOT EXISTS hive.hermes_threads (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID        NOT NULL,
    title        TEXT        NOT NULL DEFAULT '',
    created_by   TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS hermes_threads_workspace_idx
    ON hive.hermes_threads (workspace_id, created_at DESC);

CREATE TABLE IF NOT EXISTS hive.hermes_messages (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    thread_id    UUID        NOT NULL,
    workspace_id UUID        NOT NULL,
    author_id    TEXT        NOT NULL DEFAULT '',
    body         TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS hermes_messages_thread_idx
    ON hive.hermes_messages (thread_id, created_at DESC);
