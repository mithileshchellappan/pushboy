DROP INDEX IF EXISTS idx_live_activity_dispatches_pending_start;

CREATE INDEX IF NOT EXISTS idx_live_activity_dispatches_pending_start
    ON live_activity_dispatches(live_activity_job_id)
    WHERE action = 'start'
      AND status IN ('ENQUEUE_PENDING', 'QUEUED', 'IN_PROGRESS', 'DISPATCHED');
