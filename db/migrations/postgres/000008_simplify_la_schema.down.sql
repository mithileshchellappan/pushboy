DROP INDEX IF EXISTS idx_la_activities_active_user;
DROP INDEX IF EXISTS idx_la_activities_active_topic;
DROP TABLE IF EXISTS la_update_tokens;

CREATE UNIQUE INDEX IF NOT EXISTS idx_la_activities_active_topic_kind
    ON la_activities(topic_id, kind)
    WHERE topic_id IS NOT NULL AND status IN ('starting', 'active', 'ending');

CREATE TABLE IF NOT EXISTS la_topic_subscriptions (
    la_registration_id TEXT NOT NULL REFERENCES la_registrations(id) ON DELETE CASCADE,
    topic_id TEXT NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    PRIMARY KEY (la_registration_id, topic_id)
);

CREATE INDEX IF NOT EXISTS idx_la_topic_subscriptions_topic_id
    ON la_topic_subscriptions(topic_id);

CREATE TABLE IF NOT EXISTS la_instances (
    id TEXT PRIMARY KEY,
    la_id TEXT NOT NULL REFERENCES la_activities(id) ON DELETE CASCADE,
    la_registration_id TEXT NOT NULL REFERENCES la_registrations(id) ON DELETE CASCADE,
    platform TEXT NOT NULL CHECK (platform IN ('ios', 'android')),
    status TEXT NOT NULL CHECK (status IN ('pending_start', 'active', 'ended', 'failed')),
    delivery_token TEXT NOT NULL DEFAULT '',
    last_success_version BIGINT NOT NULL DEFAULT 0 CHECK (last_success_version >= 0),
    last_attempt_version BIGINT NOT NULL DEFAULT 0 CHECK (last_attempt_version >= 0),
    last_attempt_at TIMESTAMP,
    next_retry_at TIMESTAMP,
    failure_count INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE (la_id, la_registration_id)
);

CREATE INDEX IF NOT EXISTS idx_la_instances_la_id
    ON la_instances(la_id);

CREATE INDEX IF NOT EXISTS idx_la_instances_registration_id
    ON la_instances(la_registration_id);

CREATE INDEX IF NOT EXISTS idx_la_instances_retry_ready
    ON la_instances(la_id, next_retry_at);
DROP INDEX IF EXISTS idx_la_activities_active_user;
DROP INDEX IF EXISTS idx_la_activities_active_topic;
DROP TABLE IF EXISTS la_update_tokens;

CREATE UNIQUE INDEX IF NOT EXISTS idx_la_activities_active_topic_kind
    ON la_activities(topic_id, kind)
    WHERE topic_id IS NOT NULL AND status IN ('starting', 'active', 'ending');

CREATE TABLE IF NOT EXISTS la_topic_subscriptions (
    la_registration_id TEXT NOT NULL REFERENCES la_registrations(id) ON DELETE CASCADE,
    topic_id TEXT NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    PRIMARY KEY (la_registration_id, topic_id)
);

CREATE INDEX IF NOT EXISTS idx_la_topic_subscriptions_topic_id
    ON la_topic_subscriptions(topic_id);

CREATE TABLE IF NOT EXISTS la_instances (
    id TEXT PRIMARY KEY,
    la_id TEXT NOT NULL REFERENCES la_activities(id) ON DELETE CASCADE,
    la_registration_id TEXT NOT NULL REFERENCES la_registrations(id) ON DELETE CASCADE,
    platform TEXT NOT NULL CHECK (platform IN ('ios', 'android')),
    status TEXT NOT NULL CHECK (status IN ('pending_start', 'active', 'ended', 'failed')),
    delivery_token TEXT NOT NULL DEFAULT '',
    last_success_version BIGINT NOT NULL DEFAULT 0 CHECK (last_success_version >= 0),
    last_attempt_version BIGINT NOT NULL DEFAULT 0 CHECK (last_attempt_version >= 0),
    last_attempt_at TIMESTAMP,
    next_retry_at TIMESTAMP,
    failure_count INTEGER NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    UNIQUE (la_id, la_registration_id)
);

CREATE INDEX IF NOT EXISTS idx_la_instances_la_id
    ON la_instances(la_id);

CREATE INDEX IF NOT EXISTS idx_la_instances_registration_id
    ON la_instances(la_registration_id);

CREATE INDEX IF NOT EXISTS idx_la_instances_retry_ready
    ON la_instances(la_id, next_retry_at);
