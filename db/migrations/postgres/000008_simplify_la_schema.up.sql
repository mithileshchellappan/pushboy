DROP TABLE IF EXISTS la_topic_subscriptions;
DROP TABLE IF EXISTS la_instances;

CREATE TABLE la_update_tokens (
    id TEXT PRIMARY KEY,
    user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
    topic_id TEXT REFERENCES topics(id) ON DELETE CASCADE,
    platform TEXT NOT NULL CHECK (platform IN ('ios', 'android')),
    token TEXT NOT NULL,
    expires_at TIMESTAMP NOT NULL DEFAULT NOW() + INTERVAL '8 hours',
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_la_update_tokens_audience
        CHECK (
            (user_id IS NOT NULL AND topic_id IS NULL) OR
            (user_id IS NULL AND topic_id IS NOT NULL)
        ),
    CONSTRAINT uq_la_update_tokens_user_platform_token
        UNIQUE (user_id, platform, token),
    CONSTRAINT uq_la_update_tokens_topic_platform_token
        UNIQUE (topic_id, platform, token)
);

CREATE INDEX idx_la_update_tokens_topic_expires_at
    ON la_update_tokens(topic_id, expires_at);

CREATE INDEX idx_la_update_tokens_user_expires_at
    ON la_update_tokens(user_id, expires_at);

DROP INDEX IF EXISTS idx_la_activities_active_topic_kind;

CREATE UNIQUE INDEX idx_la_activities_active_topic
    ON la_activities(topic_id)
    WHERE topic_id IS NOT NULL AND status IN ('starting', 'active', 'ending');

CREATE UNIQUE INDEX idx_la_activities_active_user
    ON la_activities(user_id)
    WHERE user_id IS NOT NULL AND status IN ('starting', 'active', 'ending');
