ALTER TABLE live_activity_token_activities ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'unknown';
