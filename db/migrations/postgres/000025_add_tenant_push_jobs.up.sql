ALTER TABLE publish_jobs
    ADD COLUMN tenant_id UUID,
    ADD COLUMN user_uid UUID,
    ADD COLUMN skipped_count INTEGER NOT NULL DEFAULT 0 CHECK (skipped_count >= 0),
    ADD COLUMN unknown_count INTEGER NOT NULL DEFAULT 0 CHECK (unknown_count >= 0),
    DROP CONSTRAINT chk_job_target,
    ADD CONSTRAINT chk_job_target CHECK (
        (tenant_id IS NULL AND user_uid IS NULL AND num_nonnulls(topic_id, user_id) = 1)
        OR (topic_id IS NULL AND user_id IS NULL AND num_nonnulls(tenant_id, user_uid) = 2)
    ),
    ADD CONSTRAINT publish_jobs_tenant_user_fkey
        FOREIGN KEY (tenant_id, user_uid) REFERENCES tenant_users(tenant_id, uid) ON DELETE RESTRICT,
    ADD CONSTRAINT publish_jobs_id_tenant_key UNIQUE (id, tenant_id);

ALTER TABLE delivery_receipts
    ALTER COLUMN dispatched_at DROP NOT NULL,
    ADD COLUMN tenant_id UUID,
    ADD COLUMN device_id UUID,
    ADD COLUMN ownership_version BIGINT,
    ADD COLUMN registration_id UUID,
    ADD COLUMN registration_version BIGINT,
    ADD COLUMN provider_config_id UUID,
    ADD COLUMN credential_version BIGINT,
    ADD COLUMN claim_id UUID,
    ADD CONSTRAINT delivery_receipts_tenant_job_fkey
        FOREIGN KEY (job_id, tenant_id) REFERENCES publish_jobs(id, tenant_id) ON DELETE CASCADE,
    ADD CONSTRAINT chk_receipt_target CHECK (
        (dispatched_at IS NOT NULL AND num_nonnulls(tenant_id, device_id, ownership_version,
            registration_id, registration_version, provider_config_id, credential_version, claim_id) = 0)
        OR (num_nonnulls(tenant_id, device_id, ownership_version, registration_id,
            registration_version, provider_config_id, credential_version) = 7
            AND ownership_version >= 1 AND registration_version >= 1 AND credential_version >= 1
            AND ((status = 'PENDING' AND dispatched_at IS NULL AND claim_id IS NULL)
                OR (status = 'SENDING' AND dispatched_at IS NULL AND claim_id IS NOT NULL)
                OR (status = 'SKIPPED' AND dispatched_at IS NOT NULL)
                OR (status IN ('SUCCESS', 'FAILED', 'UNKNOWN') AND dispatched_at IS NOT NULL AND claim_id IS NOT NULL)))
    );

CREATE INDEX delivery_receipts_tenant_job_page_idx ON delivery_receipts (tenant_id, job_id, id)
    WHERE tenant_id IS NOT NULL;
