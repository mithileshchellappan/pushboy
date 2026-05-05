ALTER TABLE users
    ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at::timestamptz;

ALTER TABLE tokens
    ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at::timestamptz;

ALTER TABLE user_topic_subscriptions
    ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at::timestamptz;

ALTER TABLE publish_jobs
    ALTER COLUMN created_at TYPE TIMESTAMPTZ
    USING created_at::timestamptz;

ALTER TABLE delivery_receipts
    ALTER COLUMN dispatched_at TYPE TIMESTAMPTZ
    USING dispatched_at::timestamptz;

CREATE INDEX IF NOT EXISTS idx_users_created_id
    ON users(created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_user_topic_subscriptions_topic_created_user
    ON user_topic_subscriptions(topic_id, created_at DESC, user_id DESC);

CREATE INDEX IF NOT EXISTS idx_user_topic_subscriptions_user_topic_created
    ON user_topic_subscriptions(user_id, topic_id, created_at);

CREATE INDEX IF NOT EXISTS idx_publish_jobs_topic_effective_id
    ON publish_jobs(topic_id, COALESCE(scheduled_at, created_at) DESC, id DESC)
    WHERE topic_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_publish_jobs_topic_status_effective_id
    ON publish_jobs(topic_id, status, COALESCE(scheduled_at, created_at) DESC, id DESC)
    WHERE topic_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_publish_jobs_user_effective_id
    ON publish_jobs(user_id, COALESCE(scheduled_at, created_at) DESC, id DESC)
    WHERE user_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_publish_jobs_user_status_effective_id
    ON publish_jobs(user_id, status, COALESCE(scheduled_at, created_at) DESC, id DESC)
    WHERE user_id IS NOT NULL;
