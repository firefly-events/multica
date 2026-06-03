ALTER TABLE issue
    ALTER COLUMN start_date TYPE TIMESTAMPTZ USING start_date::timestamptz,
    ALTER COLUMN due_date TYPE TIMESTAMPTZ USING due_date::timestamptz;
