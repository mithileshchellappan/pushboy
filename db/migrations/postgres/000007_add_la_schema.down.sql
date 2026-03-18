DROP INDEX IF EXISTS idx_la_instances_retry_ready;
DROP INDEX IF EXISTS idx_la_instances_registration_id;
DROP INDEX IF EXISTS idx_la_instances_la_id;
DROP TABLE IF EXISTS la_instances;

DROP INDEX IF EXISTS idx_la_activities_external_ref;
DROP INDEX IF EXISTS idx_la_activities_user_id;
DROP INDEX IF EXISTS idx_la_activities_dispatch_ready;
DROP INDEX IF EXISTS idx_la_activities_active_topic_kind;
DROP TABLE IF EXISTS la_activities;

DROP INDEX IF EXISTS idx_la_topic_subscriptions_topic_id;
DROP TABLE IF EXISTS la_topic_subscriptions;

DROP INDEX IF EXISTS idx_la_user_topic_preferences_topic_id;
DROP TABLE IF EXISTS la_user_topic_preferences;

DROP INDEX IF EXISTS idx_la_registrations_user_id;
DROP TABLE IF EXISTS la_registrations;
