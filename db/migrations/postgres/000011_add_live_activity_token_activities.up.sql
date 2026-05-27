CREATE TABLE IF NOT EXISTS live_activity_token_activities (
    activity_id TEXT NOT NULL,
    token_id TEXT NOT NULL REFERENCES live_activity_tokens(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (activity_id, token_id)
);

CREATE INDEX IF NOT EXISTS idx_live_activity_token_activities_token_id
    ON live_activity_token_activities(token_id);
