-- Add payload column to store full notification content as JSON
ALTER TABLE publish_jobs ADD COLUMN payload JSONB;

-- Migrate existing title/body data to payload format
UPDATE publish_jobs 
SET payload = jsonb_build_object('title', COALESCE(title, ''), 'body', COALESCE(body, ''))
WHERE payload IS NULL;

-- Make payload NOT NULL after migration
ALTER TABLE publish_jobs ALTER COLUMN payload SET NOT NULL;

-- Remove old title and body columns
ALTER TABLE publish_jobs DROP COLUMN title;
ALTER TABLE publish_jobs DROP COLUMN body;









