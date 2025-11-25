-- Remove user-only tokens (those without topic_id)
DELETE FROM subscriptions WHERE topic_id IS NULL;

-- Recreate with NOT NULL constraint
CREATE TABLE subscriptions_new (
    id TEXT PRIMARY KEY,
    topic_id TEXT NOT NULL,
    platform TEXT NOT NULL,
    token TEXT NOT NULL,
    external_id TEXT,
    created_at TEXT NOT NULL,
    FOREIGN KEY(topic_id) REFERENCES topics(id) ON DELETE CASCADE,
    UNIQUE(topic_id, token)
);

INSERT INTO subscriptions_new (id, topic_id, platform, token, external_id, created_at)
SELECT id, topic_id, platform, token, external_id, created_at FROM subscriptions;

DROP TABLE subscriptions;

ALTER TABLE subscriptions_new RENAME TO subscriptions;
