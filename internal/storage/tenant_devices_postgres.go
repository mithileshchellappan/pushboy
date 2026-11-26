package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
)

var ErrTenantInstallationRetired = errors.New("tenant installation retired")

type TenantInstallation struct {
	ID               uuid.UUID `json:"id"`
	UID              uuid.UUID `json:"uid"`
	OwnershipVersion int64     `json:"ownershipVersion"`
}

type InstallationObservations struct {
	OS                     *string
	OSVersion              *string
	AppVersion             *string
	Locale                 *string
	Timezone               *string
	NotificationPermission *string
	Location               *InstallationLocation
}

type InstallationLocation struct {
	Latitude   float64
	Longitude  float64
	Source     string
	ObservedAt time.Time
}

func (s *PostgresStore) EnrollInstallation(ctx context.Context, tenantID, id uuid.UUID, observations InstallationObservations) (TenantInstallation, bool, error) {
	if id == uuid.Nil {
		return TenantInstallation{}, false, ErrInvalidTenantResource
	}
	for _, field := range []*string{observations.OS, observations.OSVersion, observations.AppVersion, observations.Locale} {
		if field != nil && !validTenantResourceText(*field, 64) {
			return TenantInstallation{}, false, ErrInvalidTenantResource
		}
	}
	if timezone := observations.Timezone; timezone != nil && !validTenantResourceText(*timezone, 128) {
		return TenantInstallation{}, false, ErrInvalidTenantResource
	}
	if permission := observations.NotificationPermission; permission != nil &&
		!slices.Contains([]string{"notDetermined", "denied", "authorized", "provisional", "ephemeral"}, *permission) {
		return TenantInstallation{}, false, ErrInvalidTenantResource
	}

	var latitude, longitude, source, observedAt any
	if location := observations.Location; location != nil {
		observedAtUTC := location.ObservedAt.UTC()
		if !(location.Latitude >= -90 && location.Latitude <= 90 && location.Longitude >= -180 && location.Longitude <= 180) ||
			!validTenantResourceText(location.Source, 64) || observedAtUTC.IsZero() ||
			observedAtUTC.Year() < 1 || observedAtUTC.Year() > 9999 {
			return TenantInstallation{}, false, ErrInvalidTenantResource
		}
		latitude, longitude, source, observedAt = location.Latitude, location.Longitude, location.Source, observedAtUTC
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return TenantInstallation{}, false, fmt.Errorf("begin installation enrollment: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('tenant-device:' || $1::uuid::text || ':' || $2::uuid::text, 0))`, tenantID, id)
	if err != nil {
		return TenantInstallation{}, false, fmt.Errorf("lock installation enrollment: %w", err)
	}

	device := TenantInstallation{ID: id}
	args := []any{tenantID, id, observations.OS, observations.OSVersion, observations.AppVersion, observations.Locale,
		observations.Timezone, observations.NotificationPermission, latitude, longitude, source, observedAt}

	err = tx.QueryRowContext(ctx, `
		UPDATE tenant_devices d SET
			os = COALESCE($3, d.os),
			os_version = COALESCE($4, d.os_version),
			app_version = COALESCE($5, d.app_version),
			locale = COALESCE($6, d.locale),
			timezone = COALESCE($7, d.timezone),
			notification_permission = COALESCE($8, d.notification_permission),
			last_seen_at = GREATEST(d.last_seen_at, clock_timestamp()),
			(location_latitude, location_longitude, location_source, location_observed_at) = (
				SELECT CASE WHEN incoming.is_newer THEN $9 ELSE d.location_latitude END,
				       CASE WHEN incoming.is_newer THEN $10 ELSE d.location_longitude END,
				       CASE WHEN incoming.is_newer THEN $11 ELSE d.location_source END,
				       CASE WHEN incoming.is_newer THEN $12 ELSE d.location_observed_at END
				FROM (SELECT $12::timestamptz IS NOT NULL AND
					(d.location_observed_at IS NULL OR $12 > d.location_observed_at) AS is_newer) incoming
			)
		WHERE d.tenant_id = $1 AND d.id = $2 AND d.retired_at IS NULL AND d.origin = 'registered'
		RETURNING d.user_uid, d.ownership_version`, args...).Scan(&device.UID, &device.OwnershipVersion)

	created := errors.Is(err, sql.ErrNoRows)
	if created {
		device.UID, err = uuid.NewRandom()
		if err != nil {
			return TenantInstallation{}, false, fmt.Errorf("generate anonymous user UID: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tenant_users (tenant_id, uid, kind) VALUES ($1, $2, 'anonymous')`, tenantID, device.UID); err != nil {
			return TenantInstallation{}, false, fmt.Errorf("create installation owner: %w", err)
		}
		err = tx.QueryRowContext(ctx, `
			INSERT INTO tenant_devices (tenant_id, id, os, os_version, app_version, locale, timezone,
				notification_permission, location_latitude, location_longitude, location_source, location_observed_at,
				user_uid, origin, last_seen_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'registered', clock_timestamp())
			ON CONFLICT (tenant_id, id) DO NOTHING
			RETURNING ownership_version`, append(args, device.UID)...).Scan(&device.OwnershipVersion)
		if errors.Is(err, sql.ErrNoRows) {
			var retired bool
			err := tx.QueryRowContext(ctx, `
				SELECT retired_at IS NOT NULL FROM tenant_devices WHERE tenant_id = $1 AND id = $2`,
				tenantID, id).Scan(&retired)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return TenantInstallation{}, false, fmt.Errorf("check unavailable installation: %w", err)
			}
			if retired {
				return TenantInstallation{}, false, ErrTenantInstallationRetired
			}
			return TenantInstallation{}, false, Errors.NotFound
		}
	}
	if err != nil {
		return TenantInstallation{}, false, fmt.Errorf("write installation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return TenantInstallation{}, false, fmt.Errorf("commit installation enrollment: %w", err)
	}
	return device, created, nil
}
