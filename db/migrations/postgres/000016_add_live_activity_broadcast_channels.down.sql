DROP TABLE IF EXISTS live_activity_channels;

ALTER TABLE live_activity_tokens
    DROP COLUMN IF EXISTS supports_broadcast_channels;
