CREATE TABLE IF NOT EXISTS live_activity_tokens (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    platform TEXT NOT NULL CHECK (platform IN ('apns', 'fcm')),
    token_type TEXT NOT NULL CHECK (token_type IN ('start', 'update')),
    token TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ,
    invalidated_at TIMESTAMPTZ,
    UNIQUE(platform, token_type, token)
);

CREATE INDEX IF NOT EXISTS idx_live_activity_tokens_user_type_active
    ON live_activity_tokens(user_id, token_type)
    WHERE invalidated_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_live_activity_tokens_expires_at
    ON live_activity_tokens(expires_at)
    WHERE expires_at IS NOT NULL AND invalidated_at IS NULL;

CREATE TABLE IF NOT EXISTS live_activity_user_topic_subscriptions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    topic_id TEXT NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE(user_id, topic_id)
);

CREATE INDEX IF NOT EXISTS idx_live_activity_user_topic_subscriptions_user_id
    ON live_activity_user_topic_subscriptions(user_id);

CREATE INDEX IF NOT EXISTS idx_live_activity_user_topic_subscriptions_topic_id
    ON live_activity_user_topic_subscriptions(topic_id);

CREATE TABLE IF NOT EXISTS live_activity_jobs (
    id TEXT PRIMARY KEY,
    activity_type TEXT NOT NULL,
    user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
    topic_id TEXT REFERENCES topics(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('active', 'closing', 'closed', 'expired', 'failed')),
    latest_payload JSONB NOT NULL,
    options JSONB,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ,
    closed_at TIMESTAMPTZ,
    CHECK (
        (user_id IS NOT NULL AND topic_id IS NULL) OR
        (user_id IS NULL AND topic_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_live_activity_jobs_active_user
    ON live_activity_jobs(activity_type, user_id)
    WHERE topic_id IS NULL AND status IN ('active', 'closing');

CREATE UNIQUE INDEX IF NOT EXISTS idx_live_activity_jobs_active_topic
    ON live_activity_jobs(activity_type, topic_id)
    WHERE user_id IS NULL AND status IN ('active', 'closing');

CREATE TABLE IF NOT EXISTS live_activity_dispatches (
    id TEXT PRIMARY KEY,
    live_activity_job_id TEXT NOT NULL REFERENCES live_activity_jobs(id) ON DELETE CASCADE,
    action TEXT NOT NULL CHECK (action IN ('start', 'update', 'end')),
    payload JSONB NOT NULL,
    options JSONB,
    status TEXT NOT NULL,
    total_count INTEGER NOT NULL,
    success_count INTEGER NOT NULL,
    failure_count INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_live_activity_dispatches_job_id
    ON live_activity_dispatches(live_activity_job_id);

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
