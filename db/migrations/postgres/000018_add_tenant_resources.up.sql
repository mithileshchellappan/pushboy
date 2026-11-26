CREATE TABLE tenant_users (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    uid UUID NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('anonymous', 'identified')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, uid)
);

CREATE TABLE tenant_user_identities (
    tenant_id UUID NOT NULL,
    user_uid UUID NOT NULL,
    namespace TEXT COLLATE "C" NOT NULL,
    external_id TEXT COLLATE "C" NOT NULL,
    PRIMARY KEY (tenant_id, namespace, external_id),
    FOREIGN KEY (tenant_id, user_uid) REFERENCES tenant_users(tenant_id, uid) ON DELETE CASCADE
);

CREATE INDEX tenant_user_identities_user_idx ON tenant_user_identities(tenant_id, user_uid);

CREATE TABLE tenant_topics (
    tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    id TEXT COLLATE "C" NOT NULL,
    name TEXT COLLATE "C" NOT NULL,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, name)
);
