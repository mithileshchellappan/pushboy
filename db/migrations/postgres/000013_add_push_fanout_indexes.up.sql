CREATE INDEX IF NOT EXISTS idx_user_topic_subscriptions_topic_user
    ON user_topic_subscriptions(topic_id, user_id);

CREATE INDEX IF NOT EXISTS idx_tokens_user_id_id_active
    ON tokens(user_id, id)
    WHERE is_deleted = FALSE;
