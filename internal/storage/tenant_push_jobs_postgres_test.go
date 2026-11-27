package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/model"
)

func tenantPushJobTestTenant(t *testing.T, store *PostgresStore) uuid.UUID {
	t.Helper()
	tenant := pushRegistrationTestTenant(t, store)
	t.Cleanup(func() {
		if _, err := store.db.Exec(`DELETE FROM publish_jobs WHERE tenant_id = $1`, tenant); err != nil {
			t.Error(err)
		}
	})
	return tenant
}

func TestPostgresTenantPushScheduledSnapshotAndLegacyIsolation(t *testing.T) {
	ctx := t.Context()
	store := tenantKeyManagementStore(t)
	tenant, otherTenant := tenantPushJobTestTenant(t, store), tenantPushJobTestTenant(t, store)
	device := enrollPushRegistrationTestDevice(t, store, tenant)
	sibling := enrollPushRegistrationTestDevice(t, store, tenant)
	lateDevice := enrollPushRegistrationTestDevice(t, store, tenant)
	changeOwner := func(device TenantInstallation, target InstallationIdentification) TenantInstallation {
		t.Helper()
		updated, err := store.IdentifyInstallation(ctx, tenant, device.ID, InstallationOwnershipChange{
			OperationID: uuid.New(), ExpectedOwnershipVersion: device.OwnershipVersion,
		}, target)
		if err != nil {
			t.Fatal(err)
		}
		return updated
	}
	device = changeOwner(device, InstallationIdentification{Identity: &TenantUserIdentity{Namespace: "test", ExternalID: "first"}})
	target := InstallationIdentification{UID: &device.UID}
	sibling = changeOwner(sibling, target)
	route := pushRegistrationTestProvider(t, store, tenant, model.APNS, "sandbox", "com.example.push")
	otherRoute := pushRegistrationTestProvider(t, store, otherTenant, model.APNS, "sandbox", "com.example.push")
	token := strings.Repeat("a1", 32)
	register := func(tenant, device, route uuid.UUID) TenantPushRegistration {
		t.Helper()
		result, err := store.RegisterPush(ctx, tenant, device, TenantPushRegistrationChange{
			OperationID: uuid.New(), ProviderConfigID: route, Token: token,
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	firstRegistration := register(tenant, device.ID, route)
	siblingToken := strings.Repeat("a2", 32)
	if _, err := store.RegisterPush(ctx, tenant, sibling.ID, TenantPushRegistrationChange{
		OperationID: uuid.New(), ProviderConfigID: route, Token: siblingToken,
	}); err != nil {
		t.Fatal(err)
	}
	foreignDevice := enrollPushRegistrationTestDevice(t, store, otherTenant)
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO tenant_users (tenant_id, uid, kind) VALUES ($1, $2, 'identified')`, otherTenant, device.UID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE tenant_devices SET user_uid = $3 WHERE tenant_id = $1 AND id = $2`, otherTenant, foreignDevice.ID, device.UID); err != nil {
		t.Fatal(err)
	}
	register(otherTenant, foreignDevice.ID, otherRoute)

	legacyID := device.UID.String()
	if _, err := store.CreateUser(ctx, &User{ID: legacyID}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.DeleteUser(context.Background(), legacyID); err != nil {
			t.Error(err)
		}
	})

	scheduledAt := time.Now().Add(-time.Minute)
	job, err := store.CreateTenantUserJob(ctx, tenant, device.UID, &model.NotificationPayload{Title: "Scoped"}, &scheduledAt)
	if err != nil || job.TenantID != tenant || job.UserID != legacyID || job.Status != model.NotificationJobStatusScheduled || job.TotalCount != 2 {
		t.Fatalf("scheduled snapshot: %+v, %v", job, err)
	}
	if _, err := store.GetTenantUserJob(ctx, otherTenant, job.ID); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("cross-tenant job lookup: %v", err)
	}
	if _, err := store.ListTenantUserJobReceipts(ctx, otherTenant, job.ID, "", 1); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("cross-tenant receipt lookup: %v", err)
	}
	if _, err := store.GetJobStatus(ctx, job.ID); !errors.Is(err, Errors.NotFound) {
		t.Fatalf("legacy lookup reached scoped job: %v", err)
	}
	if jobs, err := store.ListUserNotifications(ctx, legacyID, NotificationListQuery{Limit: 10}); err != nil || len(jobs) != 0 {
		t.Fatalf("legacy user list reached scoped job: %+v, %v", jobs, err)
	}
	if err := store.UpdateJobStatus(ctx, job.ID, model.NotificationJobStatusFailed); err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeJobDispatch(ctx, job.ID, 100); err != nil {
		t.Fatal(err)
	}

	receipts, err := store.ListTenantUserJobReceipts(ctx, tenant, job.ID, "", 1)
	if err != nil || len(receipts) != 1 || receipts[0].Status != model.DeliveryStatusPending || receipts[0].DispatchedAt != nil {
		t.Fatalf("first pending public receipt: %+v, %v", receipts, err)
	}
	page, err := store.ListTenantUserJobReceipts(ctx, tenant, job.ID, receipts[0].ID, 1)
	if err != nil || len(page) != 1 || page[0].ID == receipts[0].ID {
		t.Fatalf("second pending public receipt: %+v, %v", page, err)
	}
	movedReceipt, retainedReceipt := receipts[0], page[0]
	if movedReceipt.DeviceID != device.ID {
		movedReceipt, retainedReceipt = retainedReceipt, movedReceipt
	}
	if movedReceipt.DeviceID != device.ID || movedReceipt.RegistrationID != firstRegistration.ID ||
		retainedReceipt.DeviceID != sibling.ID {
		t.Fatalf("snapshot wrong devices: %+v, %+v", movedReceipt, retainedReceipt)
	}

	forged := model.DeliveryReceipt{ID: uuid.NewString(), JobID: job.ID, TokenID: "legacy-token", Status: model.DeliveryStatusFailed}
	if err := store.RecordDeliveryReceipt(ctx, &forged); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyPushOutcomeBatch(ctx, []model.DeliveryReceipt{forged}); err != nil {
		t.Fatal(err)
	}

	moved := changeOwner(device, InstallationIdentification{Identity: &TenantUserIdentity{Namespace: "test", ExternalID: "different"}})
	changeOwner(moved, target)
	lateDevice = changeOwner(lateDevice, target)
	if _, err := store.RegisterPush(ctx, tenant, lateDevice.ID, TenantPushRegistrationChange{
		OperationID: uuid.New(), ProviderConfigID: route, Token: strings.Repeat("a3", 32),
	}); err != nil {
		t.Fatal(err)
	}

	claimedJobs, err := store.GetScheduledJobs(ctx)
	if err != nil || len(claimedJobs) != 1 || claimedJobs[0].ID != job.ID || claimedJobs[0].TenantID != tenant ||
		claimedJobs[0].UserID != legacyID || claimedJobs[0].Status != model.NotificationJobStatusQueued {
		t.Fatalf("scheduled queue jobs: %+v, %v", claimedJobs, err)
	}
	ids, err := store.GetTenantPushRecipientBatch(ctx, tenant, job.ID, "", 3)
	if err != nil || len(ids) != 2 || ids[0] != receipts[0].ID || ids[1] != page[0].ID {
		t.Fatalf("scheduled recipients changed: %v, %v", ids, err)
	}
	receiptID := movedReceipt.ID
	movedClaim, retainedClaim := uuid.New(), uuid.New()
	if _, err := store.ClaimTenantPushRecipient(ctx, otherTenant, job.ID, receiptID, movedClaim); !errors.Is(err, ErrTenantPushRecipientDone) {
		t.Fatalf("cross-tenant send claim: %v", err)
	}
	if _, err := store.ClaimTenantPushRecipient(ctx, tenant, job.ID, receiptID, movedClaim); !errors.Is(err, ErrTenantPushRecipientStale) {
		t.Fatalf("stale ownership claim: %v", err)
	}

	if attempt, err := store.ClaimTenantPushRecipient(ctx, tenant, job.ID, retainedReceipt.ID, retainedClaim); err != nil || attempt.Token != siblingToken {
		t.Fatalf("unaffected sibling claim: %v", err)
	}
	outcome := model.DeliveryReceipt{TenantID: tenant, JobID: job.ID, ID: receiptID, ClaimID: movedClaim, Status: model.DeliveryStatusSkipped}
	success := model.DeliveryReceipt{TenantID: tenant, JobID: job.ID, ID: retainedReceipt.ID, ClaimID: retainedClaim, Status: model.DeliveryStatusSuccess}
	if err := store.ApplyPushOutcomeBatch(ctx, []model.DeliveryReceipt{outcome, success, outcome}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeTenantJobDispatch(ctx, tenant, job.ID); err != nil {
		t.Fatal(err)
	}
	finished, err := store.GetTenantUserJob(ctx, tenant, job.ID)
	if err != nil || finished.TotalCount != 2 || finished.SuccessCount != 1 || finished.FailureCount != 0 || finished.SkippedCount != 1 ||
		finished.Status != model.NotificationJobStatusCompleted || finished.CompletedAt == nil {
		t.Fatalf("finished scheduled job: %+v, %v", finished, err)
	}
	receipts, err = store.ListTenantUserJobReceipts(ctx, tenant, job.ID, "", 3)
	if err != nil || len(receipts) != 2 {
		t.Fatalf("finished scheduled receipts: %+v, %v", receipts, err)
	}
	for _, receipt := range receipts {
		if receipt.ID == receiptID && (receipt.Status != model.DeliveryStatusSkipped ||
			receipt.StatusReason != model.TenantPushReasonRecipientStale) {
			t.Fatalf("stale scheduled receipt: %+v", receipt)
		}
	}
	partialJob, err := store.CreateTenantUserJob(ctx, tenant, device.UID, &model.NotificationPayload{Title: "Partial"}, nil)
	if err != nil || partialJob.TotalCount != 3 {
		t.Fatalf("new job did not snapshot all current devices: %+v, %v", partialJob, err)
	}
	ids, err = store.GetTenantPushRecipientBatch(ctx, tenant, partialJob.ID, "", 1)
	if err != nil || len(ids) != 1 {
		t.Fatalf("fetch partial job recipient: %v, %v", ids, err)
	}
	partialClaim := uuid.New()
	if _, err := store.ClaimTenantPushRecipient(ctx, tenant, partialJob.ID, ids[0], partialClaim); err != nil {
		t.Fatal(err)
	}
	if err := store.FailTenantJobDispatch(ctx, otherTenant, partialJob.ID); err != nil {
		t.Fatal(err)
	}
	if stillQueued, err := store.GetTenantUserJob(ctx, tenant, partialJob.ID); err != nil ||
		stillQueued.Status != model.NotificationJobStatusQueued {
		t.Fatalf("foreign enqueue failure changed job: %+v, %v", stillQueued, err)
	}
	if err := store.FailTenantJobDispatch(ctx, tenant, partialJob.ID); err != nil {
		t.Fatal(err)
	}
	if failed, err := store.GetTenantUserJob(ctx, tenant, partialJob.ID); err != nil ||
		failed.Status != model.NotificationJobStatusFailed || failed.SkippedCount != 2 || failed.CompletedAt != nil {
		t.Fatalf("enqueue failure did not preserve outstanding claim: %+v, %v", failed, err)
	}

	unknown := model.DeliveryReceipt{TenantID: tenant, JobID: partialJob.ID, ID: ids[0], ClaimID: partialClaim,
		Status: model.DeliveryStatusUnknown, StatusReason: "unchecked provider reply with " + token}
	if err := store.ApplyPushOutcomeBatch(ctx, []model.DeliveryReceipt{unknown, unknown}); err != nil {
		t.Fatal(err)
	}
	if failed, err := store.GetTenantUserJob(ctx, tenant, partialJob.ID); err != nil ||
		failed.Status != model.NotificationJobStatusFailed || failed.SkippedCount != 2 ||
		failed.UnknownCount != 1 || failed.FailureCount != 0 || failed.CompletedAt == nil {
		t.Fatalf("late outcome did not settle failed job: %+v, %v", failed, err)
	}
	receipts, err = store.ListTenantUserJobReceipts(ctx, tenant, partialJob.ID, "", 3)
	if err != nil || len(receipts) != 3 || receipts[0].ID != unknown.ID ||
		receipts[0].Status != model.DeliveryStatusUnknown || receipts[0].StatusReason != model.TenantPushReasonDeliveryUnknown ||
		receipts[1].StatusReason != model.TenantPushReasonEnqueueFailed || receipts[2].StatusReason != model.TenantPushReasonEnqueueFailed {
		t.Fatalf("delayed outcome receipt or enqueue failure changed: %+v, %v", receipts, err)
	}

}

func TestPostgresTenantPushGuardedInvalidationAndDuplicateClaims(t *testing.T) {
	ctx := t.Context()
	store := tenantKeyManagementStore(t)
	tenant := tenantPushJobTestTenant(t, store)
	device := enrollPushRegistrationTestDevice(t, store, tenant)
	route := pushRegistrationTestProvider(t, store, tenant, model.FCM, "production", "push-project")
	token := "example-unredacted-fcm-token"

	register := func(prior *TenantPushRegistration) TenantPushRegistration {
		t.Helper()
		request := TenantPushRegistrationChange{OperationID: uuid.New(), ProviderConfigID: route, Token: token}
		if prior != nil {
			request.ExpectedRegistrationID = &prior.ID
			request.ExpectedRegistrationVersion = prior.RegistrationVersion
		}
		registration, err := store.RegisterPush(ctx, tenant, device.ID, request)
		if err != nil {
			t.Fatal(err)
		}
		return registration
	}
	first := register(nil)
	createJob := func() (string, string) {
		t.Helper()
		job, err := store.CreateTenantUserJob(ctx, tenant, device.UID, &model.NotificationPayload{Title: "Scoped"}, nil)
		if err != nil || job.TotalCount != 1 {
			t.Fatalf("create job: %+v, %v", job, err)
		}
		ids, err := store.GetTenantPushRecipientBatch(ctx, tenant, job.ID, "", 2)
		if err != nil || len(ids) != 1 {
			t.Fatalf("job recipients: %v, %v", ids, err)
		}
		if err := store.FinalizeTenantJobDispatch(ctx, tenant, job.ID); err != nil {
			t.Fatal(err)
		}
		return job.ID, ids[0]
	}

	jobID, receiptID := createJob()
	const count = 8
	errs := make([]error, count)
	attempts := make([]TenantPushAttempt, count)
	claims := make([]uuid.UUID, count)
	runLAChannelConcurrent(count, func(i int) {
		claims[i] = uuid.New()
		attempts[i], errs[i] = store.ClaimTenantPushRecipient(ctx, tenant, jobID, receiptID, claims[i])
	})
	claimed := 0
	var claimID uuid.UUID
	for i, err := range errs {
		switch {
		case err == nil:
			claimed++
			claimID = claims[i]
			want := TenantPushAttempt{Token: token, Platform: model.FCM,
				ProviderRef: model.ProviderRef{TenantID: tenant, RouteID: route}, CredentialVersion: 1}
			if attempts[i] != want {
				t.Fatalf("claimed wrong route or token: %+v", attempts[i])
			}
		case errors.Is(err, ErrTenantPushRecipientDone):
		default:
			t.Fatal(err)
		}
	}
	if claimed != 1 {
		t.Fatalf("concurrent claim winners: %d", claimed)
	}

	if recovered, err := store.ClaimTenantPushRecipient(ctx, tenant, jobID, receiptID, claimID); err != nil || recovered.Token != token {
		t.Fatalf("own ambiguous claim could not be recovered: %v", err)
	}
	wrongClaim := model.DeliveryReceipt{TenantID: tenant, JobID: jobID, ID: receiptID, ClaimID: uuid.New(),
		Status: model.DeliveryStatusFailed, StatusReason: model.TenantPushReasonInvalidRegistration, InvalidateRegistration: true}
	if err := store.ApplyPushOutcomeBatch(ctx, []model.DeliveryReceipt{wrongClaim}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckTenantPushRecipient(ctx, tenant, jobID, receiptID, claimID); err != nil {
		t.Fatalf("outcome from foreign claim settled the receipt: %v", err)
	}

	refreshed := register(&first)
	if refreshed.ID != first.ID || refreshed.RegistrationVersion != first.RegistrationVersion+1 {
		t.Fatalf("expected same registration with a new version: %+v", refreshed)
	}
	if _, err := store.CheckTenantPushRecipient(ctx, tenant, jobID, receiptID, claimID); !errors.Is(err, ErrTenantPushRecipientStale) {
		t.Fatalf("registration version changed after claim: %v", err)
	}

	invalid := model.DeliveryReceipt{TenantID: tenant, JobID: jobID, ID: receiptID, ClaimID: claimID, Status: model.DeliveryStatusFailed,
		StatusReason: model.TenantPushReasonInvalidRegistration, InvalidateRegistration: true}
	if err := store.ApplyPushOutcomeBatch(ctx, []model.DeliveryReceipt{invalid, invalid}); err != nil {
		t.Fatal(err)
	}
	if current, err := store.GetPushRegistration(ctx, tenant, device.ID); err != nil || current != refreshed {
		t.Fatalf("old outcome changed refreshed registration: %+v, %v", current, err)
	}
	if job, err := store.GetTenantUserJob(ctx, tenant, jobID); err != nil || job.FailureCount != 1 ||
		job.Status != model.NotificationJobStatusCompleted {
		t.Fatalf("old outcome double-counted: %+v, %v", job, err)
	}

	jobID, receiptID = createJob()
	claimID = uuid.New()
	if _, err := store.ClaimTenantPushRecipient(ctx, tenant, jobID, receiptID, claimID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE provider_configs SET credential_version = credential_version + 1
		WHERE tenant_id = $1 AND id = $2`, tenant, route); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckTenantPushRecipient(ctx, tenant, jobID, receiptID, claimID); !errors.Is(err, ErrTenantPushRecipientStale) {
		t.Fatalf("credential version changed after claim: %v", err)
	}
	invalid.JobID, invalid.ID, invalid.ClaimID = jobID, receiptID, claimID
	if err := store.ApplyPushOutcomeBatch(ctx, []model.DeliveryReceipt{invalid}); err != nil {
		t.Fatal(err)
	}
	if current, err := store.GetPushRegistration(ctx, tenant, device.ID); err != nil || current != refreshed {
		t.Fatalf("old provider route invalidated registration: %+v, %v", current, err)
	}

	jobID, receiptID = createJob()
	claimID = uuid.New()
	if attempt, err := store.ClaimTenantPushRecipient(ctx, tenant, jobID, receiptID, claimID); err != nil || attempt.CredentialVersion != 2 {
		t.Fatalf("new provider route not used: %+v, %v", attempt, err)
	}
	invalid.JobID, invalid.ID, invalid.ClaimID = jobID, receiptID, claimID
	if err := store.ApplyPushOutcomeBatch(ctx, []model.DeliveryReceipt{invalid}); err != nil {
		t.Fatal(err)
	}
	if current, err := store.GetPushRegistration(ctx, tenant, device.ID); err != nil || current.ID != refreshed.ID || current.Valid {
		t.Fatalf("current registration was not invalidated: %+v, %v", current, err)
	}

	job, err := store.CreateTenantUserJob(ctx, tenant, device.UID, &model.NotificationPayload{Title: "Empty"}, nil)
	if err != nil || job.TotalCount != 0 {
		t.Fatalf("invalidated endpoint should be excluded: %+v, %v", job, err)
	}
	if err := store.FinalizeTenantJobDispatch(ctx, tenant, job.ID); err != nil {
		t.Fatal(err)
	}
	if finished, err := store.GetTenantUserJob(ctx, tenant, job.ID); err != nil ||
		finished.Status != model.NotificationJobStatusCompleted || finished.CompletedAt == nil {
		t.Fatalf("empty job did not finish: %+v, %v", finished, err)
	}
}
