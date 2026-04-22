ALTER TABLE live_activity_jobs
    DROP CONSTRAINT IF EXISTS live_activity_jobs_closed_reason_check;

ALTER TABLE live_activity_jobs
    DROP CONSTRAINT IF EXISTS live_activity_jobs_status_check;

ALTER TABLE live_activity_jobs
    ADD CONSTRAINT live_activity_jobs_status_check
    CHECK (status IN ('active', 'closing', 'closed', 'expired', 'failed'));

UPDATE live_activity_jobs
SET status = 'expired',
    closed_reason = NULL
WHERE status = 'closed'
  AND closed_reason = 'expired';

UPDATE live_activity_jobs
SET closed_reason = NULL
WHERE closed_reason IS NOT NULL;

DROP INDEX IF EXISTS idx_live_activity_jobs_active_user;
DROP INDEX IF EXISTS idx_live_activity_jobs_active_topic;
DROP INDEX IF EXISTS idx_live_activity_jobs_activity_id;
DROP INDEX IF EXISTS idx_live_activity_tokens_update_expires_at;

CREATE UNIQUE INDEX idx_live_activity_jobs_active_user
    ON live_activity_jobs(activity_type, user_id)
    WHERE topic_id IS NULL AND status IN ('active', 'closing');

CREATE UNIQUE INDEX idx_live_activity_jobs_active_topic
    ON live_activity_jobs(activity_type, topic_id)
    WHERE user_id IS NULL AND status IN ('active', 'closing');

CREATE INDEX idx_live_activity_jobs_active_expires_at
    ON live_activity_jobs(expires_at)
    WHERE status = 'active' AND expires_at IS NOT NULL;

CREATE INDEX idx_live_activity_tokens_expires_at
    ON live_activity_tokens(expires_at)
    WHERE expires_at IS NOT NULL AND invalidated_at IS NULL;

CREATE TABLE IF NOT EXISTS live_activity_dispatch_attempts (
    id TEXT PRIMARY KEY,
    dispatch_id TEXT NOT NULL REFERENCES live_activity_dispatches(id) ON DELETE CASCADE,
    live_activity_token_id TEXT NOT NULL REFERENCES live_activity_tokens(id) ON DELETE CASCADE,
    platform TEXT NOT NULL CHECK (platform IN ('apns', 'fcm')),
    status TEXT NOT NULL,
    reason TEXT,
    sent_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_live_activity_dispatch_attempts_dispatch_id
    ON live_activity_dispatch_attempts(dispatch_id);

CREATE INDEX IF NOT EXISTS idx_live_activity_dispatch_attempts_token_id
    ON live_activity_dispatch_attempts(live_activity_token_id);

ALTER TABLE live_activity_jobs
    DROP COLUMN IF EXISTS closed_reason;

ALTER TABLE live_activity_jobs
    DROP COLUMN IF EXISTS activity_id;
