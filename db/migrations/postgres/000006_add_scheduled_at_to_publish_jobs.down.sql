DROP INDEX IF EXISTS idx_publish_jobs_scheduled;

ALTER TABLE publish_jobs DROP COLUMN scheduled_at;
