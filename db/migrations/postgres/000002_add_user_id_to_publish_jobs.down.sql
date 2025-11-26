-- Remove index
DROP INDEX IF EXISTS idx_publish_jobs_user_id;

-- Remove check constraint
ALTER TABLE publish_jobs DROP CONSTRAINT IF EXISTS chk_job_target;

-- Delete any user-targeted jobs (they would violate NOT NULL constraint)
DELETE FROM publish_jobs WHERE topic_id IS NULL;

-- Restore topic_id NOT NULL constraint
ALTER TABLE publish_jobs ALTER COLUMN topic_id SET NOT NULL;

-- Remove user_id column
ALTER TABLE publish_jobs DROP COLUMN IF EXISTS user_id;

