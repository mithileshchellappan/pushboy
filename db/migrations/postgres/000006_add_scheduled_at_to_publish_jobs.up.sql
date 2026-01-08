ALTER TABLE publish_jobs ADD COLUMN scheduled_at TIMESTAMP;

CREATE INDEX idx_publish_jobs_scheduled ON publish_jobs(status, scheduled_at) WHERE status = 'SCHEDULED';
