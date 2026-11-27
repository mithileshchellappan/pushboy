ALTER TABLE provider_configs ADD COLUMN authorization_failed BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE tenant_live_activity_channels (
    tenant_id UUID NOT NULL,
    activity_id TEXT NOT NULL CHECK (length(activity_id) BETWEEN 1 AND 128),
    topic_id TEXT NOT NULL,
    provider_config_id UUID NOT NULL,
    provider TEXT NOT NULL DEFAULT 'apns' CHECK (provider = 'apns'),
    channel_id TEXT CHECK (channel_id <> ''),
    provisioning_id UUID NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('provisioning', 'ambiguous', 'active', 'deleting')),
    deletion_attempt BIGINT NOT NULL DEFAULT 0 CHECK (deletion_attempt >= 0),
    CHECK (state NOT IN ('active', 'deleting') OR channel_id IS NOT NULL),
    CHECK (state <> 'deleting' OR deletion_attempt > 0),
    CHECK (state <> 'provisioning' OR channel_id IS NULL),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, activity_id),
    UNIQUE (tenant_id, provider_config_id, channel_id),
    FOREIGN KEY (tenant_id, topic_id) REFERENCES tenant_topics(tenant_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (tenant_id, provider_config_id, provider) REFERENCES provider_configs(tenant_id, id, provider) ON DELETE RESTRICT
);

CREATE INDEX tenant_live_activity_channels_topic_idx ON tenant_live_activity_channels(tenant_id, topic_id);
