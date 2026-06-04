-- 000011 briefly had source while this branch was deployed; keep already-run databases compatible.
ALTER TABLE live_activity_token_activities DROP COLUMN IF EXISTS source;
