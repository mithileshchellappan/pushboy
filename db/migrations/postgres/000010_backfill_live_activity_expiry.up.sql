UPDATE live_activity_tokens
SET expires_at = last_seen_at + INTERVAL '8 hours'
WHERE token_type = 'update'
  AND expires_at IS NULL;
