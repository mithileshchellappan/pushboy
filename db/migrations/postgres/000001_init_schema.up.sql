CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tokens (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    platform TEXT NOT NULL,
    token TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_tokens_user_id ON tokens(user_id);

CREATE TABLE IF NOT EXISTS topics (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS user_topic_subscriptions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    topic_id TEXT NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    UNIQUE(user_id, topic_id)
);

CREATE INDEX IF NOT EXISTS idx_user_topic_subscriptions_user_id ON user_topic_subscriptions(user_id);
CREATE INDEX IF NOT EXISTS idx_user_topic_subscriptions_topic_id ON user_topic_subscriptions(topic_id);

CREATE TABLE IF NOT EXISTS publish_jobs (
    id TEXT PRIMARY KEY,
    topic_id TEXT NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    title TEXT,
    body TEXT,
    status TEXT NOT NULL,
    total_count INTEGER NOT NULL,
    success_count INTEGER NOT NULL,
    failure_count INTEGER NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS delivery_receipts (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES publish_jobs(id) ON DELETE CASCADE,
    token_id TEXT NOT NULL,
    status TEXT NOT NULL,
    status_reason TEXT,
    dispatched_at TEXT NOT NULL
);