ALTER TABLE publish_jobs
    ALTER COLUMN completed_at TYPE TIMESTAMPTZ
    USING completed_at AT TIME ZONE current_setting('TIMEZONE');

ALTER TABLE publish_jobs
    ALTER COLUMN scheduled_at TYPE TIMESTAMPTZ
    USING scheduled_at AT TIME ZONE 'UTC';
