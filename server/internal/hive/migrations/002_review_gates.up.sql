CREATE TABLE IF NOT EXISTS hive.review_gates (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID        NOT NULL,
    epic_id      TEXT        NOT NULL,
    gate_key     TEXT        NOT NULL,
    state        TEXT        NOT NULL DEFAULT 'pending',
    evidence     JSONB       NOT NULL DEFAULT '{}',
    updated_by   TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, epic_id, gate_key)
);
