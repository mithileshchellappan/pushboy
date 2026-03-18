CREATE TABLE IF NOT EXISTS la_registrations (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    platform TEXT NOT NULL CHECK (platform IN ('ios', 'android')),
    start_token TEXT NOT NULL UNIQUE,
    auto_start_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_la_registrations_user_id ON la_registrations(user_id);

CREATE TABLE IF NOT EXISTS la_user_topic_preferences (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    topic_id TEXT NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, topic_id)
);

CREATE INDEX IF NOT EXISTS idx_la_user_topic_preferences_topic_id ON la_user_topic_preferences(topic_id);

CREATE TABLE IF NOT EXISTS la_topic_subscriptions (
    la_registration_id TEXT NOT NULL REFERENCES la_registrations(id) ON DELETE CASCADE,
    topic_id TEXT NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    PRIMARY KEY (la_registration_id, topic_id)
);

CREATE INDEX IF NOT EXISTS idx_la_topic_subscriptions_topic_id ON la_topic_subscriptions(topic_id);

CREATE TABLE IF NOT EXISTS la_activities (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    audience_kind TEXT NOT NULL CHECK (audience_kind IN ('user', 'topic')),
    user_id TEXT REFERENCES users(id) ON DELETE CASCADE,
    topic_id TEXT REFERENCES topics(id) ON DELETE CASCADE,
    external_ref TEXT,
    status TEXT NOT NULL CHECK (status IN ('starting', 'active', 'ending', 'ended', 'failed')),
    pending_event TEXT NOT NULL CHECK (pending_event IN ('start', 'update', 'end')),
    start_attributes JSONB NOT NULL DEFAULT '{}'::jsonb,
    current_state JSONB NOT NULL DEFAULT '{}'::jsonb,
    alert JSONB,
    state_version BIGINT NOT NULL DEFAULT 0 CHECK (state_version >= 0),
    claimed_version BIGINT NOT NULL DEFAULT 0 CHECK (claimed_version >= 0),
    claim_token TEXT NOT NULL DEFAULT '',
    claim_until TIMESTAMP,
    dispatch_needed BOOLEAN NOT NULL DEFAULT FALSE,
    dispatch_after TIMESTAMP NOT NULL DEFAULT NOW(),
    last_dispatch_at TIMESTAMP,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_la_activity_audience
        CHECK (
            (audience_kind = 'user' AND user_id IS NOT NULL AND topic_id IS NULL) OR
            (audience_kind = 'topic' AND topic_id IS NOT NULL AND user_id IS NULL)
        )
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_la_activities_active_topic_kind
    ON la_activities(topic_id, kind)
    WHERE topic_id IS NOT NULL AND status IN ('starting', 'active', 'ending');

CREATE INDEX IF NOT EXISTS idx_la_activities_dispatch_ready
    ON la_activities(dispatch_after, claim_until)
    WHERE dispatch_needed = TRUE;

CREATE INDEX IF NOT EXISTS idx_la_activities_user_id ON la_activities(user_id);
CREATE INDEX IF NOT EXISTS idx_la_activities_external_ref ON la_activities(external_ref) WHERE external_ref IS NOT NULL;

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

CREATE INDEX IF NOT EXISTS idx_la_instances_la_id ON la_instances(la_id);
CREATE INDEX IF NOT EXISTS idx_la_instances_registration_id ON la_instances(la_registration_id);
CREATE INDEX IF NOT EXISTS idx_la_instances_retry_ready ON la_instances(la_id, next_retry_at);
