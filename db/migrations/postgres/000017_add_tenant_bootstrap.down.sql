DO $$
BEGIN
    LOCK TABLE tenants, tenant_api_keys IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM tenants) OR EXISTS (SELECT 1 FROM tenant_api_keys) THEN
        RAISE EXCEPTION 'cannot roll back migration 17 while tenants or server keys exist'
            USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;

    DROP TABLE tenant_api_keys;
    DROP TABLE tenants;
END;
$$;
