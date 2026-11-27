CREATE TABLE tenant_push_registration_operations (
    tenant_id UUID NOT NULL,
    device_id UUID NOT NULL,
    operation_id UUID NOT NULL,
    request_digest BYTEA NOT NULL CHECK (octet_length(request_digest) = 32),
    PRIMARY KEY (tenant_id, device_id, operation_id),
    FOREIGN KEY (tenant_id, device_id) REFERENCES tenant_devices(tenant_id, id) ON DELETE RESTRICT
);

ALTER TABLE tokens
    ADD COLUMN tenant_id UUID,
    ADD COLUMN device_id UUID,
    ADD COLUMN provider_config_id UUID,
    ADD COLUMN registration_id UUID,
    ADD COLUMN token_fingerprint BYTEA,
    ADD COLUMN registration_version BIGINT,
    ADD COLUMN registration_operation_id UUID,
    ADD COLUMN superseded_at TIMESTAMPTZ,
    ALTER COLUMN user_id DROP NOT NULL,
    ADD CONSTRAINT tokens_tenant_registration_check CHECK (
        (tenant_id IS NULL AND user_id IS NOT NULL
          AND num_nonnulls(device_id, provider_config_id, registration_id, token_fingerprint,
                           registration_version, registration_operation_id, superseded_at) = 0)
        OR
        (user_id IS NULL AND num_nonnulls(tenant_id, device_id, provider_config_id, registration_id,
                                        token_fingerprint, registration_version, registration_operation_id) = 7
          AND octet_length(token_fingerprint) = 32 AND registration_version >= 1
          AND octet_length(token) BETWEEN 1 AND 4096
          AND (platform = 'fcm' OR (platform = 'apns' AND token COLLATE "C" ~ '^[0-9a-f]+$'
                                    AND length(token) % 2 = 0)))
    ),
    ADD FOREIGN KEY (tenant_id, device_id) REFERENCES tenant_devices(tenant_id, id) ON DELETE RESTRICT,
    ADD FOREIGN KEY (tenant_id, provider_config_id, platform) REFERENCES provider_configs(tenant_id, id, provider) ON DELETE RESTRICT,
    ADD FOREIGN KEY (tenant_id, device_id, registration_operation_id)
        REFERENCES tenant_push_registration_operations(tenant_id, device_id, operation_id);

CREATE UNIQUE INDEX tokens_legacy_token_idx ON tokens(token) WHERE tenant_id IS NULL;
ALTER TABLE tokens DROP CONSTRAINT tokens_token_key;

CREATE UNIQUE INDEX tokens_registration_id_idx ON tokens(registration_id) WHERE tenant_id IS NOT NULL;
CREATE UNIQUE INDEX tokens_current_registration_idx ON tokens(tenant_id, device_id)
    WHERE tenant_id IS NOT NULL AND superseded_at IS NULL;
CREATE UNIQUE INDEX tokens_active_endpoint_idx ON tokens(tenant_id, provider_config_id, token_fingerprint)
    WHERE tenant_id IS NOT NULL AND superseded_at IS NULL AND NOT is_deleted;
