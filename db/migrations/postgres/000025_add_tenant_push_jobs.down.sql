LOCK TABLE publish_jobs, delivery_receipts IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM publish_jobs WHERE tenant_id IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot roll back tenant push jobs while scoped jobs exist' USING ERRCODE = '55000';
    END IF;
END
$$;

DROP INDEX delivery_receipts_tenant_job_page_idx;

ALTER TABLE delivery_receipts
    DROP CONSTRAINT chk_receipt_target,
    DROP CONSTRAINT delivery_receipts_tenant_job_fkey,
    DROP COLUMN tenant_id,
    DROP COLUMN device_id,
    DROP COLUMN ownership_version,
    DROP COLUMN registration_id,
    DROP COLUMN registration_version,
    DROP COLUMN provider_config_id,
    DROP COLUMN credential_version,
    DROP COLUMN claim_id,
    ALTER COLUMN dispatched_at SET NOT NULL;

ALTER TABLE publish_jobs
    DROP CONSTRAINT publish_jobs_id_tenant_key,
    DROP CONSTRAINT publish_jobs_tenant_user_fkey,
    DROP CONSTRAINT chk_job_target,
    DROP COLUMN tenant_id,
    DROP COLUMN user_uid,
    DROP COLUMN skipped_count,
    DROP COLUMN unknown_count,
    ADD CONSTRAINT chk_job_target CHECK (num_nonnulls(topic_id, user_id) = 1);
