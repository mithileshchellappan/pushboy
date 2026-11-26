ALTER TABLE tenant_users
    ADD COLUMN creation_request_id UUID,
    ADD COLUMN creation_request_digest BYTEA,
    ADD CONSTRAINT tenant_users_creation_request_digest_check CHECK (
        (creation_request_id IS NULL AND creation_request_digest IS NULL)
        OR (creation_request_id IS NOT NULL AND creation_request_digest IS NOT NULL
            AND octet_length(creation_request_digest) = 32)
    );

CREATE UNIQUE INDEX tenant_users_creation_request_idx
    ON tenant_users (tenant_id, creation_request_id)
    WHERE creation_request_id IS NOT NULL;
