-- Add user_id column for user-targeted notifications
ALTER TABLE publish_jobs ADD COLUMN user_id TEXT REFERENCES users(id) ON DELETE CASCADE;

-- Make topic_id nullable (job can be either topic-based OR user-based)
ALTER TABLE publish_jobs ALTER COLUMN topic_id DROP NOT NULL;

-- Add check constraint: job must have either topic_id OR user_id (not both, not neither)
ALTER TABLE publish_jobs ADD CONSTRAINT chk_job_target 
    CHECK (
        (topic_id IS NOT NULL AND user_id IS NULL) OR 
        (topic_id IS NULL AND user_id IS NOT NULL)
    );

-- Add index for user_id lookups
CREATE INDEX IF NOT EXISTS idx_publish_jobs_user_id ON publish_jobs(user_id);

