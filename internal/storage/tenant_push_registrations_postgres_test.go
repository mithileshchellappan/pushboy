package storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/model"
)

func pushRegistrationTestTenant(t *testing.T, store *PostgresStore) uuid.UUID {
	t.Helper()
	tenant := ownershipTestTenant(t, store)
	t.Cleanup(func() {
		for _, query := range []string{
			`DELETE FROM tokens WHERE tenant_id = $1`,
			`DELETE FROM tenant_push_registration_operations WHERE tenant_id = $1`,
			`DELETE FROM provider_configs WHERE tenant_id = $1`,
		} {
			if _, err := store.db.Exec(query, tenant); err != nil {
				t.Error(err)
			}
		}
	})
	return tenant
}

func enrollPushRegistrationTestDevice(t *testing.T, store *PostgresStore, tenant uuid.UUID) TenantInstallation {
	t.Helper()
	os := "test-os"
	device, _, err := store.EnrollInstallation(t.Context(), tenant, uuid.New(), InstallationObservations{OS: &os})
	if err != nil {
		t.Fatal(err)
	}
	return device
}

func pushRegistrationTestProvider(t *testing.T, store *PostgresStore, tenant uuid.UUID, provider model.Platform, environment, app string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := store.db.ExecContext(t.Context(), `
		INSERT INTO provider_configs (tenant_id, id, provider, environment, application_id, enabled,
			credential_version, credential_key_id, credential_nonce, credential_ciphertext)
		VALUES ($1, $2, $3, $4, $5, true, 1, 'test', decode(repeat('00', 12), 'hex'), decode(repeat('00', 16), 'hex'))
	`, tenant, id, provider, environment, app)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPostgresPushRegistrationRotationAndReplay(t *testing.T) {
	ctx := t.Context()
	store := tenantKeyManagementStore(t)
	tenant := pushRegistrationTestTenant(t, store)
	device := enrollPushRegistrationTestDevice(t, store, tenant)
	provider := pushRegistrationTestProvider(t, store, tenant, model.APNS, "sandbox", "com.example.app")
	if _, err := store.GetPushRegistration(ctx, tenant, device.ID); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("never registered: %v", err)
	}
	first := TenantPushRegistrationChange{OperationID: uuid.New(), ProviderConfigID: provider, Token: strings.Repeat("Ab", 32)}
	initial, err := store.RegisterPush(ctx, tenant, device.ID, first)
	if err != nil || !initial.Valid || initial.ID == uuid.Nil || initial.RegistrationVersion != 1 || initial.Provider != model.APNS {
		t.Fatalf("initial registration: %+v, %v", initial, err)
	}
	first.Token = strings.ToLower(first.Token)
	if retry, err := store.RegisterPush(ctx, tenant, device.ID, first); err != nil || retry != initial {
		t.Fatalf("canonical replay: %+v, %v", retry, err)
	}
	invalid := first
	invalid.OperationID, invalid.Token = uuid.New(), "not hexadecimal"
	if _, err := store.RegisterPush(ctx, tenant, device.ID, invalid); !errors.Is(err, ErrInvalidTenantResource) {
		t.Fatalf("bad APNs endpoint: %v", err)
	}
	confirm := TenantPushRegistrationChange{
		OperationID: uuid.New(), ProviderConfigID: provider, Token: first.Token,
		ExpectedRegistrationID: &initial.ID, ExpectedRegistrationVersion: 1,
	}
	confirmed, err := store.RegisterPush(ctx, tenant, device.ID, confirm)
	if err != nil || confirmed.ID != initial.ID || confirmed.RegistrationVersion != 2 || !confirmed.Valid {
		t.Fatalf("confirm registration: %+v, %v", confirmed, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tokens SET is_deleted = true WHERE tenant_id = $1 AND registration_id = $2`, tenant, confirmed.ID); err != nil {
		t.Fatal(err)
	}
	confirmed.Valid = false
	if current, err := store.GetPushRegistration(ctx, tenant, device.ID); err != nil || current != confirmed {
		t.Fatalf("invalidated current registration: %+v, %v", current, err)
	}
	if retry, err := store.RegisterPush(ctx, tenant, device.ID, confirm); err != nil || retry != confirmed {
		t.Fatalf("replay revived or changed invalidated slot: %+v, %v", retry, err)
	}
	rotate := TenantPushRegistrationChange{
		OperationID: uuid.New(), ProviderConfigID: provider, Token: strings.Repeat("Cd", 32),
		ExpectedRegistrationID: &confirmed.ID, ExpectedRegistrationVersion: 2,
	}
	rotated, err := store.RegisterPush(ctx, tenant, device.ID, rotate)
	if err != nil || rotated.ID == confirmed.ID || rotated.RegistrationVersion != 3 || !rotated.Valid {
		t.Fatalf("rotate current endpoint: %+v, %v", rotated, err)
	}
	for _, stale := range []TenantPushRegistrationChange{first, confirm, {
		OperationID: first.OperationID, ProviderConfigID: provider, Token: first.Token,
		ExpectedRegistrationID: &rotated.ID, ExpectedRegistrationVersion: rotated.RegistrationVersion,
	}, {
		OperationID: uuid.New(), ProviderConfigID: provider, Token: first.Token,
		ExpectedRegistrationID: &confirmed.ID, ExpectedRegistrationVersion: rotated.RegistrationVersion,
	}} {
		if _, err := store.RegisterPush(ctx, tenant, device.ID, stale); !errors.Is(err, ErrPushRegistrationConflict) {
			t.Fatalf("stale operation %v: %v", stale.OperationID, err)
		}
	}
	var oldKept bool
	digest := sha256.Sum256([]byte(first.Token))
	err = store.db.QueryRowContext(ctx, `
		SELECT superseded_at IS NOT NULL AND is_deleted AND token = $3 AND token_fingerprint = $4 AND registration_version = 2
		FROM tokens WHERE tenant_id = $1 AND registration_id = $2
	`, tenant, initial.ID, first.Token, digest[:]).Scan(&oldKept)
	if err != nil || !oldKept {
		t.Fatalf("historical registration preserved: %v, %v", oldKept, err)
	}
	var storedOS string
	var owner uuid.UUID
	var ownershipVersion int64
	err = store.db.QueryRowContext(ctx, `
		SELECT user_uid, ownership_version, os FROM tenant_devices WHERE tenant_id = $1 AND id = $2
	`, tenant, device.ID).Scan(&owner, &ownershipVersion, &storedOS)
	if err != nil || owner != device.UID || ownershipVersion != device.OwnershipVersion || storedOS != "test-os" {
		t.Fatalf("ownership or metadata changed: %v, %v, %v, %v", owner, ownershipVersion, storedOS, err)
	}
}

func TestPostgresConcurrentPushRegistrationScopes(t *testing.T) {
	ctx := t.Context()
	store := tenantKeyManagementStore(t)
	tenant, other := pushRegistrationTestTenant(t, store), pushRegistrationTestTenant(t, store)
	first, second := enrollPushRegistrationTestDevice(t, store, tenant), enrollPushRegistrationTestDevice(t, store, tenant)
	otherDevice := enrollPushRegistrationTestDevice(t, store, other)
	provider := pushRegistrationTestProvider(t, store, tenant, model.FCM, "production", "project-one")
	otherRoute := pushRegistrationTestProvider(t, store, tenant, model.FCM, "production", "project-two")
	foreignProvider := pushRegistrationTestProvider(t, store, other, model.FCM, "production", "project-one")
	token := " example.Endpoint/AbC::1 "
	ids := []uuid.UUID{first.ID, second.ID}
	changes := []TenantPushRegistrationChange{
		{OperationID: uuid.New(), ProviderConfigID: provider, Token: token},
		{OperationID: uuid.New(), ProviderConfigID: provider, Token: token},
	}
	registrations := make([]TenantPushRegistration, 2)
	errs := make([]error, 2)
	runLAChannelConcurrent(2, func(i int) {
		registrations[i], errs[i] = store.RegisterPush(ctx, tenant, ids[i], changes[i])
	})
	winner, loser := 0, 1
	if errs[1] == nil {
		winner, loser = 1, 0
	}
	if errs[winner] != nil || registrations[winner].RegistrationVersion != 1 || !errors.Is(errs[loser], ErrPushRegistrationConflict) {
		t.Fatalf("same endpoint outcomes: %+v, %v", registrations, errs)
	}
	if _, err := store.GetPushRegistration(ctx, tenant, ids[loser]); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("conflicting claim created a registration: %v", err)
	}
	var exactToken string
	if err := store.db.QueryRowContext(ctx, `SELECT token FROM tokens WHERE registration_id = $1`, registrations[winner].ID).Scan(&exactToken); err != nil || exactToken != token {
		t.Fatalf("FCM endpoint round trip was not exact: %v", err)
	}
	foreign := TenantPushRegistrationChange{OperationID: uuid.New(), ProviderConfigID: foreignProvider, Token: token}
	if _, err := store.RegisterPush(ctx, tenant, ids[loser], foreign); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("foreign provider configuration: %v", err)
	}
	if _, err := store.RegisterPush(ctx, tenant, otherDevice.ID, changes[loser]); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("foreign installation: %v", err)
	}
	if _, err := store.RegisterPush(ctx, other, otherDevice.ID, foreign); err != nil {
		t.Fatalf("same endpoint in its independent tenant: %v", err)
	}
	changes[loser].ProviderConfigID = otherRoute
	onOtherRoute, err := store.RegisterPush(ctx, tenant, ids[loser], changes[loser])
	if err != nil || onOtherRoute.ID == registrations[winner].ID {
		t.Fatalf("same endpoint on another provider route: %+v, %v", onOtherRoute, err)
	}
	runLAChannelConcurrent(2, func(i int) {
		registrations[i], errs[i] = store.RegisterPush(ctx, tenant, ids[loser], changes[loser])
	})
	for i, retry := range registrations {
		if errs[i] != nil || retry != onOtherRoute {
			t.Fatalf("concurrent exact retry: %+v, %v", retry, errs[i])
		}
	}
}

func TestPostgresPushRegistrationPreservesLegacyFanoutAndOutcomes(t *testing.T) {
	ctx := t.Context()
	store := tenantKeyManagementStore(t)
	tenant := pushRegistrationTestTenant(t, store)
	device := enrollPushRegistrationTestDevice(t, store, tenant)
	provider := pushRegistrationTestProvider(t, store, tenant, model.APNS, "sandbox", "com.example.app")
	userID, topicID := device.UID.String(), uuid.NewString()
	if _, err := store.CreateUser(ctx, &User{ID: userID}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.DeleteUser(context.Background(), userID); err != nil {
			t.Error(err)
		}
	})
	if err := store.CreateTopic(ctx, &Topic{ID: topicID, Name: topicID}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.DeleteTopic(context.Background(), topicID); err != nil {
			t.Error(err)
		}
	})
	if _, err := store.SubscribeUserToTopic(ctx, &UserTopicSubscription{ID: uuid.NewString(), UserID: userID, TopicID: topicID}); err != nil {
		t.Fatal(err)
	}
	token := strings.ReplaceAll(uuid.NewString(), "-", "") + strings.Repeat("ac", 16)
	legacy := Token{ID: uuid.NewString(), UserID: userID, Platform: model.APNS, Token: token}
	if _, err := store.CreateToken(ctx, &legacy); err != nil {
		t.Fatal(err)
	}
	registered, err := store.RegisterPush(ctx, tenant, device.ID, TenantPushRegistrationChange{
		OperationID: uuid.New(), ProviderConfigID: provider, Token: token,
	})
	if err != nil {
		t.Fatalf("tenant registration with same value as unscoped token: %v", err)
	}
	if _, err := store.CreateToken(ctx, &Token{ID: uuid.NewString(), UserID: userID, Platform: model.FCM, Token: token}); !errors.Is(err, Errors.AlreadyExists) {
		t.Fatalf("legacy global uniqueness changed: %v", err)
	}
	for _, page := range []struct {
		scope string
		read  func(context.Context, string, string, int) (*TokenBatch, error)
		count func(context.Context, string) (int, error)
	}{
		{userID, store.GetTokenBatchForUser, store.GetTokenCountForUser},
		{topicID, store.GetTokenBatchForTopic, store.GetTokenCountForTopic},
	} {
		if count, err := page.count(ctx, page.scope); err != nil || count != 1 {
			t.Fatalf("legacy audience count: %d, %v", count, err)
		}
		batch, err := page.read(ctx, page.scope, "", 1)
		if err != nil || len(batch.Tokens) != 1 || batch.HasMore || batch.Tokens[0].ID != legacy.ID {
			t.Fatalf("legacy fanout recipients changed: %+v, %v", batch, err)
		}
	}
	if rows, err := store.GetTokensByUserID(ctx, userID); err != nil || len(rows) != 1 || rows[0].ID != legacy.ID {
		t.Fatalf("legacy token list leaked: %+v, %v", rows, err)
	}
	if err := store.DeleteToken(ctx, registered.ID.String()); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("legacy delete by public ID reached tenant registration: %v", err)
	}
	var internalID string
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM tokens WHERE registration_id = $1`, registered.ID).Scan(&internalID); err != nil || internalID == registered.ID.String() {
		t.Fatalf("tenant registration shares public and internal IDs: %v", err)
	}
	if err := store.DeleteToken(ctx, internalID); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("legacy delete by internal ID reached tenant registration: %v", err)
	}
	if err := store.SoftDeleteToken(ctx, internalID); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("legacy provider invalidation reached tenant registration: %v", err)
	}
	if err := store.BulkSoftDeleteToken(ctx, []string{internalID, legacy.ID}); err != nil {
		t.Fatal(err)
	}
	if current, err := store.GetPushRegistration(ctx, tenant, device.ID); err != nil || current != registered {
		t.Fatalf("legacy invalidation changed tenant registration: %+v, %v", current, err)
	}
	if count, err := store.GetTokenCountForTopic(ctx, topicID); err != nil || count != 0 {
		t.Fatalf("legacy invalidation lost effect: %d, %v", count, err)
	}
	job, err := store.CreatePublishJob(ctx, &PublishJob{ID: uuid.NewString(), TopicID: topicID, Status: model.NotificationJobStatusQueued})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeJobDispatch(ctx, job.ID, 1); err != nil {
		t.Fatal(err)
	}
	receipt := model.DeliveryReceipt{ID: uuid.NewString(), JobID: job.ID, TokenID: legacy.ID, Status: model.DeliveryStatusFailed}
	if err := store.ApplyPushOutcomeBatch(ctx, []model.DeliveryReceipt{receipt}); err != nil {
		t.Fatal(err)
	}
	finished, err := store.GetJobStatus(ctx, job.ID)
	if err != nil || finished.Status != model.NotificationJobStatusCompleted || finished.FailureCount != 1 {
		t.Fatalf("legacy job outcome changed: %+v, %v", finished, err)
	}
	var receiptToken string
	if err := store.db.QueryRowContext(ctx, `SELECT token_id FROM delivery_receipts WHERE id = $1`, receipt.ID).Scan(&receiptToken); err != nil || receiptToken != legacy.ID {
		t.Fatalf("legacy receipt changed: %v", err)
	}
}
