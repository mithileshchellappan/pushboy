-- Fanout hot path: live update tokens only (dead rows dominate the table,
-- so the full-table scan in the token batch join is mostly wasted work).
CREATE INDEX IF NOT EXISTS idx_live_activity_tokens_active_update
    ON live_activity_tokens(id)
    WHERE invalidated_at IS NULL AND token_type = 'update';

-- Scope lookup: the start-pending EXISTS probe otherwise scans every
-- dispatch of the job (one row per update, hundreds per race).
CREATE INDEX IF NOT EXISTS idx_live_activity_dispatches_pending_start
    ON live_activity_dispatches(live_activity_job_id)
    WHERE action = 'start' AND status IN ('QUEUED', 'IN_PROGRESS', 'DISPATCHED');
