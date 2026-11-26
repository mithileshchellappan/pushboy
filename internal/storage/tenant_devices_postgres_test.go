package storage

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

func TestPostgresInstallationEnrollment(t *testing.T) {
	ctx := t.Context()
	store := tenantKeyManagementStore(t)
	createTenant := func() uuid.UUID {
		tenant := createTenantResourceTestTenant(t, store)
		t.Cleanup(func() {
			if _, err := store.db.Exec(`DELETE FROM tenant_devices WHERE tenant_id = $1`, tenant); err != nil {
				t.Error(err)
			}
		})
		return tenant
	}
	tenantID, otherTenantID := createTenant(), createTenant()
	id := uuid.New()

	os, osVersion, appVersion, locale, timezone := "iOS", "26.1", "1.0", "en-DE", "Europe/Berlin"
	permission := "provisional"
	observedAt := time.Date(2026, 11, 26, 5, 0, 0, 0, time.UTC)
	observations := InstallationObservations{
		OS: &os, OSVersion: &osVersion, AppVersion: &appVersion, Locale: &locale,
		Timezone: &timezone, NotificationPermission: &permission,
		Location: &InstallationLocation{Latitude: 1.5, Longitude: -5.25, Source: "device", ObservedAt: observedAt},
	}

	const callers = 8
	type enrollment struct {
		device TenantInstallation
		first  bool
		err    error
	}
	results := make([]enrollment, callers)
	runLAChannelConcurrent(callers, func(i int) {
		results[i].device, results[i].first, results[i].err = store.EnrollInstallation(ctx, tenantID, id, observations)
	})
	initial := results[0].device
	creations := 0
	for _, result := range results {
		if result.err != nil || result.device != initial {
			t.Fatalf("concurrent enrollment = %+v, %v, want %+v", result.device, result.err, initial)
		}
		if result.first {
			creations++
		}
	}
	if creations != 1 || initial.ID != id || initial.UID == uuid.Nil || initial.OwnershipVersion != 1 {
		t.Fatalf("creation count = %d, association = %+v", creations, initial)
	}
	if user, err := store.GetTenantUser(ctx, tenantID, initial.UID); err != nil || user.Kind != "anonymous" {
		t.Fatalf("initial owner = %+v, %v", user, err)
	}

	other, created, err := store.EnrollInstallation(ctx, otherTenantID, id, InstallationObservations{})
	if err != nil || !created || other.ID != id || other.UID == initial.UID || other.OwnershipVersion != 1 {
		t.Fatalf("same installation ID in another tenant = %+v, %t, %v", other, created, err)
	}
	var metadataAbsent bool
	err = store.db.QueryRowContext(ctx, `
		SELECT num_nonnulls(os, os_version, app_version, locale, timezone, notification_permission,
			location_latitude, location_longitude, location_source, location_observed_at, installation_created_at) = 0
			AND last_seen_at IS NOT NULL
		FROM tenant_devices WHERE tenant_id = $1 AND id = $2`, otherTenantID, id).Scan(&metadataAbsent)
	if err != nil || !metadataAbsent {
		t.Fatalf("missing observations remained unknown = %t, %v", metadataAbsent, err)
	}

	_, err = store.db.ExecContext(ctx, `UPDATE tenant_devices SET user_uid = $1 WHERE tenant_id = $2 AND id = $3`, other.UID, tenantID, id)
	var postgresErr *pq.Error
	if !errors.As(err, &postgresErr) || postgresErr.Code != "23503" {
		t.Fatalf("cross-tenant owner assignment = %v, want foreign key violation", err)
	}
	assertCounts := func(wantUsers, wantDevices int) {
		t.Helper()
		var users, devices int
		err := store.db.QueryRowContext(ctx, `
			SELECT (SELECT count(*) FROM tenant_users WHERE tenant_id = $1),
			       (SELECT count(*) FROM tenant_devices WHERE tenant_id = $1)`, tenantID).Scan(&users, &devices)
		if err != nil || users != wantUsers || devices != wantDevices {
			t.Fatalf("tenant users = %d, devices = %d, error = %v, want %d users and %d devices", users, devices, err, wantUsers, wantDevices)
		}
	}
	assertCounts(1, 1)

	account, err := store.CreateTenantUser(ctx, tenantID, &TenantUserIdentity{Namespace: "account", ExternalID: "42"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	past := observedAt.Add(-time.Hour)
	if _, err := store.db.ExecContext(ctx, `
		UPDATE tenant_devices SET user_uid = $1, ownership_version = 2, last_seen_at = $2
		WHERE tenant_id = $3 AND id = $4`, account.UID, past, tenantID, id); err != nil {
		t.Fatal(err)
	}
	changedVersion := "1.1"
	staleLocation := InstallationLocation{Latitude: 76, Longitude: 83, Source: "delayed", ObservedAt: past}
	current, created, err := store.EnrollInstallation(ctx, tenantID, id, InstallationObservations{AppVersion: &changedVersion, Location: &staleLocation})
	want := TenantInstallation{ID: id, UID: account.UID, OwnershipVersion: 2}
	if err != nil || created || current != want {
		t.Fatalf("refresh after account switch = %+v, %t, %v, want %+v", current, created, err, want)
	}
	assertObservations := func(location InstallationLocation) {
		t.Helper()
		var match bool
		err := store.db.QueryRowContext(ctx, `
			SELECT os = $3 AND os_version = $4 AND app_version = $5 AND locale = $6 AND timezone = $7
				AND notification_permission = $8 AND last_seen_at > $9 AND location_latitude = $10 AND location_longitude = $11
				AND location_source = $12 AND location_observed_at = $13
			FROM tenant_devices WHERE tenant_id = $1 AND id = $2`, tenantID, id, os, osVersion, changedVersion,
			locale, timezone, permission, past, location.Latitude, location.Longitude, location.Source, location.ObservedAt.UTC()).Scan(&match)
		if err != nil || !match {
			t.Fatalf("persisted observations match = %t, %v", match, err)
		}
	}
	assertObservations(*observations.Location)

	offset := time.FixedZone("", 16*60*60)
	freshLocation := InstallationLocation{Latitude: 0, Longitude: 0, Source: "gps", ObservedAt: observedAt.Add(time.Minute).In(offset)}
	if current, created, err := store.EnrollInstallation(ctx, tenantID, id, InstallationObservations{Location: &freshLocation}); err != nil || created || current != want {
		t.Fatalf("refresh with newer location = %+v, %t, %v", current, created, err)
	}
	assertObservations(freshLocation)
	assertCounts(2, 1)

	if _, err := store.db.ExecContext(ctx, `UPDATE tenant_devices SET retired_at = now() WHERE tenant_id = $1 AND id = $2`, tenantID, id); err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.EnrollInstallation(ctx, tenantID, id, InstallationObservations{}); !errors.Is(err, ErrTenantInstallationRetired) || created {
		t.Fatalf("refresh of retired installation = %t, %v, want ErrTenantInstallationRetired", created, err)
	}
	if replay, created, err := store.EnrollInstallation(ctx, otherTenantID, id, InstallationObservations{}); err != nil || created || replay != other {
		t.Fatalf("refresh in tenant with non-retired installation = %+v, %t, %v", replay, created, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tenant_devices SET origin = 'legacy', retired_at = NULL WHERE tenant_id = $1 AND id = $2`, tenantID, id); err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.EnrollInstallation(ctx, tenantID, id, InstallationObservations{}); !errors.Is(err, Errors.NotFound) || created {
		t.Fatalf("refusing to claim provisional installation = %t, %v, want NotFound", created, err)
	}
	for _, coordinate := range []float64{91, math.NaN(), math.Inf(1)} {
		badLocation := &InstallationLocation{Latitude: coordinate, Longitude: 0, Source: "gps", ObservedAt: observedAt}
		if _, created, err := store.EnrollInstallation(ctx, tenantID, uuid.New(), InstallationObservations{Location: badLocation}); !errors.Is(err, ErrInvalidTenantResource) || created {
			t.Fatalf("invalid coordinate %v = %t, %v, want ErrInvalidTenantResource", coordinate, created, err)
		}
	}
	outOfRange := InstallationLocation{Source: "gps", ObservedAt: time.Date(1, 1, 1, 15, 0, 0, 0, offset)}
	if _, created, err := store.EnrollInstallation(ctx, tenantID, uuid.New(), InstallationObservations{Location: &outOfRange}); !errors.Is(err, ErrInvalidTenantResource) || created {
		t.Fatalf("location timestamp outside the supported UTC range = %t, %v, want ErrInvalidTenantResource", created, err)
	}
	assertCounts(2, 1)
}
