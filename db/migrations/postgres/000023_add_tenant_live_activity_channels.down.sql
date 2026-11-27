DO $$
BEGIN
    LOCK TABLE tenant_live_activity_channels IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM tenant_live_activity_channels) THEN
        RAISE EXCEPTION 'cannot roll back migration 23 while tenant live activity channels exist'
            USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;

    DROP TABLE tenant_live_activity_channels;
    ALTER TABLE provider_configs DROP COLUMN authorization_failed;
END;
$$;
