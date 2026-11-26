DO $$
BEGIN
    LOCK TABLE tenant_users IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM tenant_users WHERE creation_request_id IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot roll back migration 19 while tenant user creation requests exist'
            USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;

    ALTER TABLE tenant_users
        DROP COLUMN creation_request_id,
        DROP COLUMN creation_request_digest;
END;
$$;
