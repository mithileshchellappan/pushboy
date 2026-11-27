package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func ownershipTestTenant(t *testing.T, store *PostgresStore) uuid.UUID {
	t.Helper()
	tenant := createTenantResourceTestTenant(t, store)
	t.Cleanup(func() {
		for _, query := range []string{
			`UPDATE tenant_devices SET last_ownership_operation_id = NULL WHERE tenant_id = $1`,
			`DELETE FROM tenant_device_ownership_operations WHERE tenant_id = $1`,
			`DELETE FROM tenant_devices WHERE tenant_id = $1`,
		} {
			if _, err := store.db.Exec(query, tenant); err != nil {
				t.Error(err)
			}
		}
	})
	return tenant
}

func TestPostgresInstallationOwnershipSwitchReplayAndLogout(t *testing.T) {
	ctx := t.Context()
	store := tenantKeyManagementStore(t)
	tenant, otherTenant := ownershipTestTenant(t, store), ownershipTestTenant(t, store)
	enroll := func(inTenant uuid.UUID) TenantInstallation {
		t.Helper()
		os := "iOS"
		device, created, err := store.EnrollInstallation(ctx, inTenant, uuid.New(), InstallationObservations{OS: &os})
		if err != nil || !created {
			t.Fatalf("enroll = %+v, %t, %v", device, created, err)
		}
		return device
	}
	change := func(version int64) InstallationOwnershipChange {
		return InstallationOwnershipChange{OperationID: uuid.New(), ExpectedOwnershipVersion: version}
	}

	first, second, foreign := enroll(tenant), enroll(tenant), enroll(otherTenant)
	accountA := InstallationIdentification{Identity: &TenantUserIdentity{Namespace: "customer", ExternalID: "A"}}
	accountB := InstallationIdentification{Identity: &TenantUserIdentity{Namespace: "customer", ExternalID: "B"}}
	promote := change(1)
	a, err := store.IdentifyInstallation(ctx, tenant, first.ID, promote, accountA)
	if err != nil || a.UID != first.UID || a.OwnershipVersion != 2 {
		t.Fatalf("anonymous promotion = %+v, %v", a, err)
	}
	// Simulate a lost response by resubmitting the exact identify operation.
	if replay, err := store.IdentifyInstallation(ctx, tenant, first.ID, promote, accountA); err != nil || replay != a {
		t.Fatalf("promotion replay = %+v, %v", replay, err)
	}
	if _, err := store.IdentifyInstallation(ctx, tenant, first.ID, promote, accountB); !errors.Is(err, ErrInstallationOwnershipConflict) {
		t.Fatalf("reuse of latest UUID with different payload = %v", err)
	}
	changedVersion := promote
	changedVersion.ExpectedOwnershipVersion = a.OwnershipVersion
	if _, err := store.IdentifyInstallation(ctx, tenant, first.ID, changedVersion, accountA); !errors.Is(err, ErrInstallationOwnershipConflict) {
		t.Fatalf("reuse of latest UUID with different version = %v", err)
	}
	aByUID := InstallationIdentification{UID: &a.UID}
	otherA, err := store.IdentifyInstallation(ctx, tenant, second.ID, change(1), aByUID)
	if err != nil || otherA.UID != a.UID || otherA.OwnershipVersion != 2 {
		t.Fatalf("second installation into A = %+v, %v", otherA, err)
	}
	switchB := change(2)
	b, err := store.IdentifyInstallation(ctx, tenant, first.ID, switchB, accountB)
	if err != nil || b.UID == a.UID || b.OwnershipVersion != 3 {
		t.Fatalf("A → B = %+v, %v", b, err)
	}
	backA, err := store.IdentifyInstallation(ctx, tenant, first.ID, change(3), accountA)
	if err != nil || backA.UID != a.UID || backA.OwnershipVersion != 4 {
		t.Fatalf("B → A = %+v, %v", backA, err)
	}
	if same, err := store.IdentifyInstallation(ctx, tenant, first.ID, change(4), aByUID); err != nil || same != backA {
		t.Fatalf("identify current account = %+v, %v", same, err)
	}
	if _, err := store.IdentifyInstallation(ctx, tenant, first.ID, switchB, accountB); !errors.Is(err, ErrInstallationOwnershipConflict) {
		t.Fatalf("old A → B replay = %v", err)
	}
	switchB.ExpectedOwnershipVersion = 4
	if _, err := store.IdentifyInstallation(ctx, tenant, first.ID, switchB, accountB); !errors.Is(err, ErrInstallationOwnershipConflict) {
		t.Fatalf("old operation with current version = %v", err)
	}
	if _, err := store.IdentifyInstallation(ctx, tenant, first.ID, change(3), accountB); !errors.Is(err, ErrInstallationOwnershipConflict) {
		t.Fatalf("new operation with stale version = %v", err)
	}
	logout := change(4)
	anonymous, err := store.LogoutInstallation(ctx, tenant, first.ID, logout)
	if err != nil || anonymous.UID == a.UID || anonymous.UID == b.UID || anonymous.OwnershipVersion != 5 {
		t.Fatalf("logout = %+v, %v", anonymous, err)
	}
	if replay, err := store.LogoutInstallation(ctx, tenant, first.ID, logout); err != nil || replay != anonymous {
		t.Fatalf("logout replay = %+v, %v", replay, err)
	}
	// The identify request was prepared before a second logout arrived.
	delayedIdentify := change(anonymous.OwnershipVersion)
	secondLogout := change(anonymous.OwnershipVersion)
	newGeneration, err := store.LogoutInstallation(ctx, tenant, first.ID, secondLogout)
	if err != nil || newGeneration.UID != anonymous.UID || newGeneration.OwnershipVersion != 6 {
		t.Fatalf("already anonymous logout = %+v, %v", newGeneration, err)
	}
	if replay, err := store.LogoutInstallation(ctx, tenant, first.ID, secondLogout); err != nil || replay != newGeneration {
		t.Fatalf("already anonymous logout replay = %+v, %v", replay, err)
	}
	// An exact duplicate is stale once a later operation commits, even when
	// the intervening logout kept the anonymous owner.
	if _, err := store.LogoutInstallation(ctx, tenant, first.ID, logout); !errors.Is(err, ErrInstallationOwnershipConflict) {
		t.Fatalf("overtaken logout replay = %v", err)
	}
	if _, err := store.IdentifyInstallation(ctx, tenant, first.ID, delayedIdentify, accountA); !errors.Is(err, ErrInstallationOwnershipConflict) {
		t.Fatalf("delayed identify after anonymous logout = %v", err)
	}
	if refreshed, created, err := store.EnrollInstallation(ctx, tenant, first.ID, InstallationObservations{}); err != nil || created || refreshed != newGeneration {
		t.Fatalf("installation refresh after anonymous logout = %+v, %t, %v", refreshed, created, err)
	}
	if _, err := store.IdentifyInstallation(ctx, tenant, first.ID, promote, accountA); !errors.Is(err, ErrInstallationOwnershipConflict) {
		t.Fatalf("replay identify after logout = %v", err)
	}
	if _, err := store.IdentifyInstallation(ctx, otherTenant, foreign.ID, change(1), aByUID); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("cross-tenant user = %v", err)
	}
	if _, err := store.LogoutInstallation(ctx, otherTenant, first.ID, change(5)); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("cross-tenant installation = %v", err)
	}
	var firstOS string
	if err := store.db.QueryRowContext(ctx, `SELECT os FROM tenant_devices WHERE tenant_id = $1 AND id = $2`, tenant, first.ID).Scan(&firstOS); err != nil || firstOS != "iOS" {
		t.Fatalf("installation metadata changed: %q, %v", firstOS, err)
	}
	if current, _, err := store.EnrollInstallation(ctx, tenant, second.ID, InstallationObservations{}); err != nil || current != otherA {
		t.Fatalf("second installation changed during switch: %+v, %v", current, err)
	}
	for label, uid := range map[string]uuid.UUID{"A": a.UID, "B": b.UID} {
		var resolved uuid.UUID
		err := store.db.QueryRowContext(ctx, `
			SELECT user_uid FROM tenant_user_identities WHERE tenant_id = $1 AND namespace = 'customer' AND external_id = $2
		`, tenant, label).Scan(&resolved)
		if err != nil || uid != resolved {
			t.Fatalf("identity %s changed: %v, %v", label, resolved, err)
		}
	}
	var users int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tenant_users WHERE tenant_id = $1`, tenant).Scan(&users); err != nil || users != 4 {
		t.Fatalf("user count after switches and retries = %d, %v", users, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tenant_devices SET retired_at = clock_timestamp() WHERE tenant_id = $1 AND id = $2`, tenant, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LogoutInstallation(ctx, tenant, first.ID, change(6)); !errors.Is(err, ErrTenantInstallationRetired) {
		t.Fatalf("retired installation logout = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tenant_devices SET origin = 'legacy' WHERE tenant_id = $1 AND id = $2`, tenant, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.IdentifyInstallation(ctx, tenant, second.ID, change(2), accountB); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("legacy installation identify = %v", err)
	}
	if _, err := store.LogoutInstallation(ctx, tenant, second.ID, change(2)); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("legacy installation logout = %v", err)
	}
	var owner uuid.UUID
	var version int64
	var operations int
	err = store.db.QueryRowContext(ctx, `
		SELECT d.user_uid, d.ownership_version, (SELECT COUNT(*) FROM tenant_device_ownership_operations
			WHERE tenant_id = d.tenant_id AND device_id = d.id)
		FROM tenant_devices d WHERE d.tenant_id = $1 AND d.id = $2
	`, tenant, second.ID).Scan(&owner, &version, &operations)
	if err != nil || owner != otherA.UID || version != otherA.OwnershipVersion || operations != 1 {
		t.Fatalf("legacy installation changed: owner %v, version %d, operations %d, err %v", owner, version, operations, err)
	}
}

func TestPostgresConcurrentInstallationPromotionResolvesOneAccount(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	store := tenantKeyManagementStore(t)
	tenant := ownershipTestTenant(t, store)
	var firstOwners [2]uuid.UUID
	var ids [2]uuid.UUID
	for i := range ids {
		ids[i] = uuid.New()
		device, _, err := store.EnrollInstallation(ctx, tenant, ids[i], InstallationObservations{})
		if err != nil {
			t.Fatal(err)
		}
		firstOwners[i] = device.UID
	}
	barrier, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer barrier.Rollback()
	var blocker int
	if err := barrier.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	if _, err := barrier.ExecContext(ctx, `
		SELECT uid FROM tenant_users WHERE tenant_id = $1 AND uid IN ($2, $3) ORDER BY uid FOR UPDATE
	`, tenant, firstOwners[0], firstOwners[1]); err != nil {
		t.Fatal(err)
	}

	type result struct {
		device TenantInstallation
		err    error
	}
	results := make(chan result, 2)
	target := InstallationIdentification{Identity: &TenantUserIdentity{Namespace: "customer", ExternalID: "same verified identity"}}
	for _, id := range ids {
		go func() {
			device, err := store.IdentifyInstallation(ctx, tenant, id, InstallationOwnershipChange{uuid.New(), 1}, target)
			results <- result{device, err}
		}()
	}
	for {
		var pending int
		err := store.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			  AND $1 = ANY(pg_blocking_pids(pid))
			  AND query LIKE '%SELECT uid, kind FROM tenant_users%'
		`, blocker).Scan(&pending)
		if err != nil {
			t.Fatalf("wait for both attempts to read the missing identity: %v", err)
		}
		if pending == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := barrier.Commit(); err != nil {
		t.Fatal(err)
	}
	left, right := <-results, <-results
	if left.err != nil || right.err != nil || left.device.UID != right.device.UID ||
		left.device.ID == right.device.ID || left.device.OwnershipVersion != 2 || right.device.OwnershipVersion != 2 {
		t.Fatalf("concurrent identify = %+v and %+v", left, right)
	}
	if left.device.UID != firstOwners[0] && left.device.UID != firstOwners[1] {
		t.Fatalf("fresh identity failed to promote either anonymous owner: %v", left.device.UID)
	}
	var users, identified, linked int
	err = store.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE kind = 'identified'),
		       (SELECT COUNT(*) FROM tenant_user_identities WHERE tenant_id = $1)
		FROM tenant_users WHERE tenant_id = $1
	`, tenant).Scan(&users, &identified, &linked)
	if err != nil || users != 2 || identified != 1 || linked != 1 {
		t.Fatalf("users, identified and linked = %d, %d, %d, %v", users, identified, linked, err)
	}
}
