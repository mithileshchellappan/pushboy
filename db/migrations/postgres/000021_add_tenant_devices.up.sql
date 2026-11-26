CREATE TABLE tenant_devices (
    tenant_id UUID NOT NULL,
    id UUID NOT NULL,
    user_uid UUID NOT NULL,
    ownership_version BIGINT NOT NULL DEFAULT 1 CHECK (ownership_version >= 1),
    origin TEXT NOT NULL CHECK (origin IN ('registered', 'legacy')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    installation_created_at TIMESTAMPTZ,
    last_seen_at TIMESTAMPTZ,
    retired_at TIMESTAMPTZ,
    os TEXT CHECK (char_length(os) BETWEEN 1 AND 64),
    os_version TEXT CHECK (char_length(os_version) BETWEEN 1 AND 64),
    app_version TEXT CHECK (char_length(app_version) BETWEEN 1 AND 64),
    locale TEXT CHECK (char_length(locale) BETWEEN 1 AND 64),
    timezone TEXT CHECK (char_length(timezone) BETWEEN 1 AND 128),
    notification_permission TEXT CHECK (notification_permission IN (
        'notDetermined', 'denied', 'authorized', 'provisional', 'ephemeral'
    )),
    location_latitude DOUBLE PRECISION CHECK (location_latitude BETWEEN -90 AND 90),
    location_longitude DOUBLE PRECISION CHECK (location_longitude BETWEEN -180 AND 180),
    location_source TEXT CHECK (char_length(location_source) BETWEEN 1 AND 64),
    location_observed_at TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, user_uid) REFERENCES tenant_users(tenant_id, uid) ON DELETE RESTRICT,
    CHECK (num_nonnulls(location_latitude, location_longitude, location_source, location_observed_at) IN (0, 4))
);

CREATE INDEX tenant_devices_active_user_idx ON tenant_devices(tenant_id, user_uid, id) WHERE retired_at IS NULL;
