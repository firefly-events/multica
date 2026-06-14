CREATE TABLE IF NOT EXISTS hive.plugin_skill_catalog_state (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID        NOT NULL UNIQUE,
    catalog_version TEXT        NOT NULL DEFAULT '0.0.0',
    last_browsed_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
