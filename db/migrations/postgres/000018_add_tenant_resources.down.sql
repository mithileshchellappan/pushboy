DO $$
BEGIN
    LOCK TABLE tenant_users, tenant_user_identities, tenant_topics IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM tenant_users)
        OR EXISTS (SELECT 1 FROM tenant_user_identities)
        OR EXISTS (SELECT 1 FROM tenant_topics)
    THEN
        RAISE EXCEPTION 'cannot roll back migration 18 while tenant users, identities, or topics exist'
            USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;

    DROP TABLE tenant_topics;
    DROP TABLE tenant_user_identities;
    DROP TABLE tenant_users;
END;
$$;
