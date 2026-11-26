CREATE TABLE provider_configs (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    id UUID NOT NULL,
    provider TEXT NOT NULL,
    environment TEXT NOT NULL,
    application_id TEXT NOT NULL,
    enabled BOOLEAN NOT NULL,
    credential_version BIGINT NOT NULL CHECK (credential_version > 0),
    credential_key_id TEXT NOT NULL CHECK (credential_key_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    credential_nonce BYTEA NOT NULL CHECK (octet_length(credential_nonce) = 12),
    credential_ciphertext BYTEA NOT NULL CHECK (octet_length(credential_ciphertext) >= 16),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, id, provider),
    CHECK (
        (provider = 'apns' AND environment IN ('sandbox', 'production')
         AND length(application_id) <= 255 AND application_id ~ '^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*$')
        OR
        (provider = 'fcm' AND environment = 'production' AND application_id ~ '^[a-z][a-z0-9-]{4,28}[a-z0-9]$')
    )
);

CREATE UNIQUE INDEX provider_configs_namespace_idx ON provider_configs (
    tenant_id, provider, environment,
    (CASE WHEN provider = 'apns' THEN lower(application_id COLLATE "C") ELSE application_id END)
);
