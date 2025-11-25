-- SQLite doesn't support ALTER COLUMN, so we need to recreate the table
CREATE TABLE subscriptions_new (
    id TEXT PRIMARY KEY,
    topic_id TEXT,
    platform TEXT NOT NULL,
    token TEXT NOT NULL,
    external_id TEXT,
    created_at TEXT NOT NULL,
    FOREIGN KEY(topic_id) REFERENCES topics(id) ON DELETE CASCADE
);

INSERT INTO subscriptions_new (id, topic_id, platform, token, external_id, created_at)
SELECT id, topic_id, platform, token, external_id, created_at FROM subscriptions;

DROP TABLE subscriptions;

ALTER TABLE subscriptions_new RENAME TO subscriptions;

CREATE UNIQUE INDEX idx_subscriptions_topic_token ON subscriptions(topic_id, token) WHERE topic_id IS NOT NULL;
