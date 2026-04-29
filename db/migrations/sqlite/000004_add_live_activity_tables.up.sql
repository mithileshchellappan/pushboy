CREATE TABLE IF NOT EXISTS live_activity_tokens (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    platform TEXT NOT NULL CHECK (platform IN ('apns', 'fcm')),
    token_type TEXT NOT NULL CHECK (token_type IN ('start', 'update')),
    token TEXT NOT NULL,
    created_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    expires_at TEXT,
    invalidated_at TEXT,
    UNIQUE(platform, token_type, token),
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_live_activity_tokens_user_type_active
    ON live_activity_tokens(user_id, token_type)
    WHERE invalidated_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_live_activity_tokens_expires_at
    ON live_activity_tokens(expires_at)
    WHERE expires_at IS NOT NULL AND invalidated_at IS NULL;

CREATE TABLE IF NOT EXISTS live_activity_user_topic_subscriptions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    topic_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(user_id, topic_id),
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY(topic_id) REFERENCES topics(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_live_activity_user_topic_subscriptions_user_id
    ON live_activity_user_topic_subscriptions(user_id);

CREATE INDEX IF NOT EXISTS idx_live_activity_user_topic_subscriptions_topic_id
    ON live_activity_user_topic_subscriptions(topic_id);

CREATE TABLE IF NOT EXISTS live_activity_jobs (
    id TEXT PRIMARY KEY,
    activity_type TEXT NOT NULL,
    user_id TEXT,
    topic_id TEXT,
    status TEXT NOT NULL CHECK (status IN ('active', 'closing', 'closed', 'expired', 'failed')),
    latest_payload TEXT NOT NULL,
    options TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    expires_at TEXT,
    closed_at TEXT,
    CHECK (
        (user_id IS NOT NULL AND topic_id IS NULL) OR
        (user_id IS NULL AND topic_id IS NOT NULL)
    ),
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY(topic_id) REFERENCES topics(id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_live_activity_jobs_active_user
    ON live_activity_jobs(activity_type, user_id)
    WHERE topic_id IS NULL AND status IN ('active', 'closing');

CREATE UNIQUE INDEX IF NOT EXISTS idx_live_activity_jobs_active_topic
    ON live_activity_jobs(activity_type, topic_id)
    WHERE user_id IS NULL AND status IN ('active', 'closing');

CREATE TABLE IF NOT EXISTS live_activity_dispatches (
    id TEXT PRIMARY KEY,
    live_activity_job_id TEXT NOT NULL,
    action TEXT NOT NULL CHECK (action IN ('start', 'update', 'end')),
    payload TEXT NOT NULL,
    options TEXT,
    status TEXT NOT NULL,
    total_count INTEGER NOT NULL,
    success_count INTEGER NOT NULL,
    failure_count INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    completed_at TEXT,
    FOREIGN KEY(live_activity_job_id) REFERENCES live_activity_jobs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_live_activity_dispatches_job_id
    ON live_activity_dispatches(live_activity_job_id);

CREATE TABLE IF NOT EXISTS live_activity_dispatch_attempts (
    id TEXT PRIMARY KEY,
    dispatch_id TEXT NOT NULL,
    live_activity_token_id TEXT NOT NULL,
    platform TEXT NOT NULL CHECK (platform IN ('apns', 'fcm')),
    status TEXT NOT NULL,
    reason TEXT,
    sent_at TEXT NOT NULL,
    FOREIGN KEY(dispatch_id) REFERENCES live_activity_dispatches(id) ON DELETE CASCADE,
    FOREIGN KEY(live_activity_token_id) REFERENCES live_activity_tokens(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_live_activity_dispatch_attempts_dispatch_id
    ON live_activity_dispatch_attempts(dispatch_id);

CREATE INDEX IF NOT EXISTS idx_live_activity_dispatch_attempts_token_id
    ON live_activity_dispatch_attempts(live_activity_token_id);
