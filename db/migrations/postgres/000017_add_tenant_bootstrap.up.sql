CREATE TABLE tenants (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL UNIQUE CHECK (length(btrim(name)) BETWEEN 1 AND 160),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    disabled_at TIMESTAMPTZ
);

CREATE TABLE tenant_api_keys (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    secret_hash BYTEA NOT NULL CHECK (octet_length(secret_hash) = 32),
    scopes TEXT[] NOT NULL CHECK (cardinality(scopes) > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    CHECK (expires_at IS NULL OR expires_at > created_at)
);

CREATE INDEX tenant_api_keys_tenant_id_idx ON tenant_api_keys(tenant_id);
