-- Restore title and body columns
ALTER TABLE publish_jobs ADD COLUMN title TEXT;
ALTER TABLE publish_jobs ADD COLUMN body TEXT;

-- Migrate payload data back to title/body
UPDATE publish_jobs 
SET title = payload->>'title',
    body = payload->>'body';

-- Drop payload column
ALTER TABLE publish_jobs DROP COLUMN payload;



