DO $$
BEGIN
    LOCK TABLE tokens, tenant_push_registration_operations IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM tokens WHERE tenant_id IS NOT NULL)
        OR EXISTS (SELECT 1 FROM tenant_push_registration_operations) THEN
        RAISE EXCEPTION 'cannot roll back migration 24 while tenant push registrations exist'
            USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;

    ALTER TABLE tokens ADD CONSTRAINT tokens_token_key UNIQUE (token);
    ALTER TABLE tokens
        DROP COLUMN tenant_id,
        DROP COLUMN device_id,
        DROP COLUMN provider_config_id,
        DROP COLUMN registration_id,
        DROP COLUMN token_fingerprint,
        DROP COLUMN registration_version,
        DROP COLUMN registration_operation_id,
        DROP COLUMN superseded_at,
        ALTER COLUMN user_id SET NOT NULL;
    DROP TABLE tenant_push_registration_operations;
END;
$$;
