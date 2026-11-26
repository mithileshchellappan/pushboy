DO $$
BEGIN
    LOCK TABLE provider_configs IN ACCESS EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM provider_configs) THEN
        RAISE EXCEPTION 'cannot roll back migration 20 while provider configs exist'
            USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;

    DROP TABLE provider_configs;
END;
$$;
