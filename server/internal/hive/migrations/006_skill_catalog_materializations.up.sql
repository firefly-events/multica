CREATE TABLE IF NOT EXISTS hive.plugin_skill_catalog_materializations (
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID        NOT NULL,
    skill_id         UUID        NOT NULL,
    catalog_key      TEXT        NOT NULL,
    catalog_version  TEXT        NOT NULL,
    state            TEXT        NOT NULL DEFAULT 'active',
    materialized_by  TEXT        NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(workspace_id, catalog_key)
);
