ALTER TABLE live_activity_tokens
    ADD COLUMN IF NOT EXISTS supports_broadcast_channels BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS live_activity_channels (
    activity_id TEXT PRIMARY KEY,
    topic_id TEXT NOT NULL REFERENCES topics(id) ON DELETE RESTRICT,
    channel_id TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL
);
