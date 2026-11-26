CREATE TABLE tenant_device_ownership_operations (
    tenant_id UUID NOT NULL,
    device_id UUID NOT NULL,
    operation_id UUID NOT NULL,
    request_digest BYTEA NOT NULL CHECK (octet_length(request_digest) = 32),
    ownership_version BIGINT NOT NULL CHECK (ownership_version >= 1),
    PRIMARY KEY (tenant_id, device_id, operation_id),
    FOREIGN KEY (tenant_id, device_id) REFERENCES tenant_devices(tenant_id, id) ON DELETE RESTRICT
);

ALTER TABLE tenant_devices
    ADD COLUMN last_ownership_operation_id UUID,
    ADD FOREIGN KEY (tenant_id, id, last_ownership_operation_id)
        REFERENCES tenant_device_ownership_operations(tenant_id, device_id, operation_id);
