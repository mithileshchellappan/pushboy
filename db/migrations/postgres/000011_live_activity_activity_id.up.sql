ALTER TABLE live_activity_jobs
    ADD COLUMN IF NOT EXISTS activity_id TEXT;

UPDATE live_activity_jobs
SET activity_id = id
WHERE activity_id IS NULL;

ALTER TABLE live_activity_jobs
    ALTER COLUMN activity_id SET NOT NULL;

ALTER TABLE live_activity_jobs
    ADD COLUMN IF NOT EXISTS closed_reason TEXT;

UPDATE live_activity_jobs
SET status = 'closed',
    closed_reason = 'expired',
    closed_at = COALESCE(closed_at, NOW()),
    updated_at = NOW()
WHERE status = 'expired';

UPDATE live_activity_jobs
SET closed_reason = 'ended'
WHERE status = 'closed'
  AND closed_reason IS NULL;

UPDATE live_activity_jobs
SET closed_reason = 'failed'
WHERE status = 'failed'
  AND closed_reason IS NULL;

DROP INDEX IF EXISTS idx_live_activity_jobs_active_user;
DROP INDEX IF EXISTS idx_live_activity_jobs_active_topic;
DROP INDEX IF EXISTS idx_live_activity_jobs_active_expires_at;
DROP INDEX IF EXISTS idx_live_activity_tokens_expires_at;

DROP INDEX IF EXISTS idx_live_activity_dispatch_attempts_dispatch_id;
DROP INDEX IF EXISTS idx_live_activity_dispatch_attempts_token_id;
DROP TABLE IF EXISTS live_activity_dispatch_attempts;

DROP INDEX IF EXISTS idx_live_activity_jobs_activity_id;
CREATE UNIQUE INDEX idx_live_activity_jobs_activity_id
    ON live_activity_jobs(activity_id);

CREATE UNIQUE INDEX idx_live_activity_jobs_active_user
    ON live_activity_jobs(activity_type, user_id)
    WHERE topic_id IS NULL AND closed_at IS NULL AND status IN ('active', 'closing');

CREATE UNIQUE INDEX idx_live_activity_jobs_active_topic
    ON live_activity_jobs(activity_type, topic_id)
    WHERE user_id IS NULL AND closed_at IS NULL AND status IN ('active', 'closing');

CREATE INDEX idx_live_activity_tokens_update_expires_at
    ON live_activity_tokens(expires_at)
    WHERE token_type = 'update' AND expires_at IS NOT NULL AND invalidated_at IS NULL;

ALTER TABLE live_activity_jobs
    DROP CONSTRAINT IF EXISTS live_activity_jobs_status_check;

ALTER TABLE live_activity_jobs
    ADD CONSTRAINT live_activity_jobs_status_check
    CHECK (status IN ('active', 'closing', 'closed', 'failed'));

ALTER TABLE live_activity_jobs
    DROP CONSTRAINT IF EXISTS live_activity_jobs_closed_reason_check;

ALTER TABLE live_activity_jobs
    ADD CONSTRAINT live_activity_jobs_closed_reason_check
    CHECK (
        closed_reason IS NULL OR
        closed_reason IN ('ended', 'superseded', 'expired', 'failed')
    );
