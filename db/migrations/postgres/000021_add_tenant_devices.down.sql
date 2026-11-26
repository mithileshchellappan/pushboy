DO $$
BEGIN
    LOCK TABLE tenant_devices IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM tenant_devices) THEN
        RAISE EXCEPTION 'cannot roll back migration 21 while tenant devices exist'
            USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;

    DROP TABLE tenant_devices;
END;
$$;
