ALTER TABLE hive.hermes_messages
    ADD COLUMN IF NOT EXISTS model TEXT;
