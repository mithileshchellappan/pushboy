DO $$
BEGIN
    LOCK TABLE tenant_devices, tenant_device_ownership_operations IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM tenant_device_ownership_operations) THEN
        RAISE EXCEPTION 'cannot roll back migration 22 while installation ownership operations exist'
            USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;
    ALTER TABLE tenant_devices DROP COLUMN last_ownership_operation_id;
    DROP TABLE tenant_device_ownership_operations;
END;
$$;
