CREATE TABLE IF NOT EXISTS hive.personal_queue_items (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID        NOT NULL,
    assignee_id  UUID        NOT NULL,
    ref_kind     TEXT        NOT NULL DEFAULT '',
    ref_id       TEXT        NOT NULL DEFAULT '',
    title        TEXT        NOT NULL DEFAULT '',
    status       TEXT        NOT NULL DEFAULT 'pending',
    meta         JSONB       NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS personal_queue_items_workspace_assignee
    ON hive.personal_queue_items (workspace_id, assignee_id);
