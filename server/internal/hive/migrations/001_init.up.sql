CREATE SCHEMA IF NOT EXISTS hive;

CREATE TABLE IF NOT EXISTS hive.schema_migrations (
    version     TEXT        PRIMARY KEY,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS hive.epic_nodes (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID        NOT NULL,
    epic_id      TEXT        NOT NULL,
    kind         TEXT        NOT NULL DEFAULT 'epic',
    payload      JSONB       NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
