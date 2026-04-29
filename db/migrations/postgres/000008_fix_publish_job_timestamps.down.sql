ALTER TABLE publish_jobs
    ALTER COLUMN scheduled_at TYPE TIMESTAMP
    USING scheduled_at AT TIME ZONE 'UTC';

ALTER TABLE publish_jobs
    ALTER COLUMN completed_at TYPE TIMESTAMP
    USING completed_at AT TIME ZONE current_setting('TIMEZONE');
