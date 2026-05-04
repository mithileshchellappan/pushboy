DROP INDEX IF EXISTS idx_publish_jobs_user_status_effective_id;
DROP INDEX IF EXISTS idx_publish_jobs_user_effective_id;
DROP INDEX IF EXISTS idx_publish_jobs_topic_status_effective_id;
DROP INDEX IF EXISTS idx_publish_jobs_topic_effective_id;
DROP INDEX IF EXISTS idx_user_topic_subscriptions_user_topic_created;
DROP INDEX IF EXISTS idx_user_topic_subscriptions_topic_created_user;
DROP INDEX IF EXISTS idx_users_created_id;

ALTER TABLE delivery_receipts
    ALTER COLUMN dispatched_at TYPE TEXT
    USING dispatched_at::text;

ALTER TABLE publish_jobs
    ALTER COLUMN created_at TYPE TEXT
    USING created_at::text;

ALTER TABLE user_topic_subscriptions
    ALTER COLUMN created_at TYPE TEXT
    USING created_at::text;

ALTER TABLE tokens
    ALTER COLUMN created_at TYPE TEXT
    USING created_at::text;

ALTER TABLE users
    ALTER COLUMN created_at TYPE TEXT
    USING created_at::text;
