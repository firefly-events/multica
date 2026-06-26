ALTER TABLE hive.hermes_messages
    ADD COLUMN IF NOT EXISTS role           TEXT    NOT NULL DEFAULT 'assistant',
    ADD COLUMN IF NOT EXISTS tokens_used    INTEGER,
    ADD COLUMN IF NOT EXISTS context_window INTEGER;

ALTER TABLE hive.hermes_threads
    ADD COLUMN IF NOT EXISTS model        TEXT,
    ADD COLUMN IF NOT EXISTS tokens_total INTEGER;
