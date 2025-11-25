CREATE TABLE IF NOT EXISTS publish_jobs (
    id TEXT PRIMARY KEY,
    topic_id TEXT NOT NULL,
    status TEXT NOT NULL,
    total_count INTEGER NOT NULL,
    success_count INTEGER NOT NULL,
    failure_count INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY(topic_id) REFERENCES topics(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS delivery_receipts (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL,
    subscription_id TEXT NOT NULL,
    status TEXT NOT NULL,
    status_reason TEXT,
    dispatched_at TEXT NOT NULL,
    FOREIGN KEY(job_id) REFERENCES publish_jobs(id) ON DELETE CASCADE
);