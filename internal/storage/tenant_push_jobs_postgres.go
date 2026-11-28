package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/model"
)

var (
	ErrTenantPushRecipientStale = errors.New("tenant push recipient changed")
	ErrTenantPushRecipientDone  = errors.New("tenant push recipient already claimed or finished")
)

type TenantJobReceipt struct {
	ID             string
	DeviceID       uuid.UUID
	RegistrationID uuid.UUID
	Status         model.DeliveryStatus
	StatusReason   string
	DispatchedAt   *time.Time
}

type TenantPushAttempt struct {
	Token             string
	Platform          model.Platform
	ProviderRef       model.ProviderRef
	CredentialVersion int64
}

func (s *PostgresStore) CreateTenantUserJob(ctx context.Context, tenantID, userUID uuid.UUID, payload *model.NotificationPayload, scheduledAt *time.Time) (*PublishJob, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("serialize tenant push payload: %w", err)
	}
	jobID := uuid.NewString()
	status := model.NotificationJobStatusQueued
	if scheduledAt != nil {
		status = model.NotificationJobStatusScheduled
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		INSERT INTO publish_jobs (id, tenant_id, user_uid, payload, status, total_count, success_count,
			failure_count, created_at, scheduled_at)
		SELECT $3, u.tenant_id, u.uid, $4, $5, 0, 0, 0, clock_timestamp(), $6
		FROM tenant_users u JOIN tenants a ON a.id = u.tenant_id
		WHERE u.tenant_id = $1 AND u.uid = $2 AND a.disabled_at IS NULL
		FOR KEY SHARE OF u`,
		tenantID, userUID, jobID, payloadJSON, status, optionalTime(scheduledAt))
	if err != nil {
		return nil, fmt.Errorf("create tenant push job: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if inserted == 0 {
		return nil, Errors.NotFound
	}

	job, err := scanPublishJob(tx.QueryRowContext(ctx, `
		WITH recipients AS (
			INSERT INTO delivery_receipts (id, job_id, token_id, status, tenant_id, device_id,
				ownership_version, registration_id, registration_version, provider_config_id, credential_version)
			SELECT gen_random_uuid()::text, $3, t.id, 'PENDING', d.tenant_id, d.id,
				d.ownership_version, t.registration_id, t.registration_version, p.id, p.credential_version
			FROM tenant_devices d
			JOIN tokens t ON t.tenant_id = d.tenant_id AND t.device_id = d.id
			JOIN provider_configs p ON p.tenant_id = t.tenant_id AND p.id = t.provider_config_id AND p.provider = t.platform
			WHERE d.tenant_id = $1 AND d.user_uid = $2 AND d.origin = 'registered' AND d.retired_at IS NULL
				AND t.superseded_at IS NULL AND NOT t.is_deleted AND p.enabled AND NOT p.authorization_failed
			RETURNING id
		)
		UPDATE publish_jobs
		SET total_count = (SELECT count(*) FROM recipients)
		WHERE tenant_id = $1 AND id = $3
		RETURNING id, '', user_uid::text, payload, status, total_count, success_count, failure_count,
			created_at, completed_at, scheduled_at, tenant_id, skipped_count, unknown_count`, tenantID, userUID, jobID))
	if err != nil {
		return nil, fmt.Errorf("snapshot tenant push recipients: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit tenant push job: %w", err)
	}
	return job, nil
}

func (s *PostgresStore) GetTenantUserJob(ctx context.Context, tenantID uuid.UUID, jobID string) (*PublishJob, error) {
	job, err := scanPublishJob(s.db.QueryRowContext(ctx, `
		SELECT id, '', user_uid::text, payload, status, total_count, success_count, failure_count,
			created_at, completed_at, scheduled_at, tenant_id, skipped_count, unknown_count
		FROM publish_jobs
		WHERE tenant_id = $1 AND id = $2`, tenantID, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Errors.NotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read tenant push job: %w", err)
	}
	return job, nil
}

func (s *PostgresStore) ListTenantUserJobReceipts(ctx context.Context, tenantID uuid.UUID, jobID, cursor string, limit int) ([]TenantJobReceipt, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM publish_jobs WHERE tenant_id = $1 AND id = $2)`, tenantID, jobID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check tenant push job: %w", err)
	}
	if !exists {
		return nil, Errors.NotFound
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, device_id, registration_id, status, COALESCE(status_reason, ''), dispatched_at
		FROM delivery_receipts
		WHERE tenant_id = $1 AND job_id = $2 AND id > $3
		ORDER BY id LIMIT $4`, tenantID, jobID, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("list tenant push receipts: %w", err)
	}
	defer rows.Close()
	receipts := make([]TenantJobReceipt, 0)
	for rows.Next() {
		var receipt TenantJobReceipt
		var dispatchedAt sql.NullTime
		if err := rows.Scan(&receipt.ID, &receipt.DeviceID, &receipt.RegistrationID, &receipt.Status, &receipt.StatusReason, &dispatchedAt); err != nil {
			return nil, fmt.Errorf("read tenant push receipt: %w", err)
		}
		receipt.DispatchedAt = nullTimePtr(dispatchedAt)
		receipts = append(receipts, receipt)
	}
	return receipts, rows.Err()
}

func (s *PostgresStore) GetTenantPushRecipientBatch(ctx context.Context, tenantID uuid.UUID, jobID, cursor string, batchSize int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM delivery_receipts
		WHERE tenant_id = $1 AND job_id = $2 AND id > $3 AND status = 'PENDING'
		ORDER BY id LIMIT $4`, tenantID, jobID, cursor, batchSize)
	if err != nil {
		return nil, fmt.Errorf("list tenant push recipient IDs: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read tenant push recipient ID: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

var (
	tenantPushRecipientSelect    = tenantPushRecipientQuery("", "", "", "")
	tenantPushAdmissionSelect    = tenantPushRecipientQuery("FOR SHARE OF d", "FOR SHARE OF t", "FOR SHARE OF p", "FOR SHARE OF a")
	tenantPushInvalidationSelect = tenantPushRecipientQuery("FOR SHARE OF d", "FOR NO KEY UPDATE OF t", "FOR SHARE OF p", "FOR SHARE OF a")
)

func tenantPushRecipientQuery(installationLock, registrationLock, routeLock, tenantLock string) string {
	return fmt.Sprintf(`
		WITH recipient AS MATERIALIZED (
			SELECT r.tenant_id, r.device_id, r.ownership_version, r.token_id, r.registration_id,
				r.registration_version, r.provider_config_id, r.credential_version, j.user_uid
			FROM delivery_receipts r
			JOIN publish_jobs j ON j.id = r.job_id AND j.tenant_id = r.tenant_id
			WHERE r.tenant_id = $1 AND r.job_id = $2 AND r.id = $3 AND r.claim_id = $4 AND r.status = 'SENDING'
		), installation AS MATERIALIZED (
			SELECT r.* FROM recipient r
			JOIN tenant_devices d ON d.tenant_id = r.tenant_id AND d.id = r.device_id AND d.user_uid = r.user_uid
				AND d.ownership_version = r.ownership_version AND d.origin = 'registered' AND d.retired_at IS NULL
			%s
		), registration AS MATERIALIZED (
			SELECT d.tenant_id, d.provider_config_id, d.credential_version, t.token, t.platform
			FROM installation d
			JOIN tokens t ON t.id = d.token_id AND t.tenant_id = d.tenant_id AND t.device_id = d.device_id
				AND t.registration_id = d.registration_id AND t.registration_version = d.registration_version
				AND t.provider_config_id = d.provider_config_id AND t.superseded_at IS NULL AND NOT t.is_deleted
			%s
		), route AS MATERIALIZED (
			SELECT t.tenant_id, t.token, t.platform, p.id, p.credential_version
			FROM registration t
			JOIN provider_configs p ON p.tenant_id = t.tenant_id AND p.id = t.provider_config_id AND p.provider = t.platform
				AND p.credential_version = t.credential_version AND p.enabled AND NOT p.authorization_failed
			%s
		)
		SELECT r.token, r.platform, r.id, r.credential_version
		FROM route r JOIN tenants a ON a.id = r.tenant_id AND a.disabled_at IS NULL
		%s`, installationLock, registrationLock, routeLock, tenantLock)
}

type tenantPushRecipientReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readTenantPushRecipient(ctx context.Context, db tenantPushRecipientReader, tenantID uuid.UUID, jobID, receiptID string, claimID uuid.UUID, lockForInvalidation bool) (TenantPushAttempt, error) {
	query := tenantPushRecipientSelect
	if lockForInvalidation {
		query = tenantPushInvalidationSelect
	}
	return scanTenantPushAttempt(db.QueryRowContext(ctx, query, tenantID, jobID, receiptID, claimID), tenantID)
}

func scanTenantPushAttempt(row *sql.Row, tenantID uuid.UUID) (TenantPushAttempt, error) {
	attempt := TenantPushAttempt{ProviderRef: model.ProviderRef{TenantID: tenantID}}
	err := row.Scan(&attempt.Token, &attempt.Platform, &attempt.ProviderRef.RouteID, &attempt.CredentialVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantPushAttempt{}, ErrTenantPushRecipientStale
	}
	if err != nil {
		return TenantPushAttempt{}, fmt.Errorf("check tenant push recipient: %w", err)
	}
	return attempt, nil
}

func (s *PostgresStore) ClaimTenantPushRecipient(ctx context.Context, tenantID uuid.UUID, jobID, receiptID string, claimID uuid.UUID) (TenantPushAttempt, error) {
	if claimID == uuid.Nil {
		return TenantPushAttempt{}, ErrInvalidTenantResource
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TenantPushAttempt{}, err
	}
	defer tx.Rollback()

	var status model.NotificationJobStatus
	err = tx.QueryRowContext(ctx, `
		SELECT status FROM publish_jobs WHERE tenant_id = $1 AND id = $2 FOR NO KEY UPDATE`, tenantID, jobID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantPushAttempt{}, ErrTenantPushRecipientDone
	}
	if err != nil {
		return TenantPushAttempt{}, fmt.Errorf("lock tenant push job: %w", err)
	}
	claimable := status == model.NotificationJobStatusQueued || status == model.NotificationJobStatusInProgress ||
		status == model.NotificationJobStatusDispatched
	result, err := tx.ExecContext(ctx, `
		UPDATE delivery_receipts SET status = 'SENDING', claim_id = $4
		WHERE tenant_id = $1 AND job_id = $2 AND id = $3
			AND ((status = 'PENDING' AND $5) OR (status = 'SENDING' AND claim_id = $4))`,
		tenantID, jobID, receiptID, claimID, claimable)
	if err != nil {
		return TenantPushAttempt{}, fmt.Errorf("claim tenant push recipient: %w", err)
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return TenantPushAttempt{}, err
	}
	if claimed == 0 {
		return TenantPushAttempt{}, ErrTenantPushRecipientDone
	}

	attempt, attemptErr := readTenantPushRecipient(ctx, tx, tenantID, jobID, receiptID, claimID, false)
	if attemptErr != nil && !errors.Is(attemptErr, ErrTenantPushRecipientStale) {
		return TenantPushAttempt{}, attemptErr
	}
	if err := tx.Commit(); err != nil {
		return TenantPushAttempt{}, fmt.Errorf("commit tenant push recipient claim: %w", err)
	}
	return attempt, attemptErr
}

func (s *PostgresStore) CheckTenantPushRecipient(ctx context.Context, tenantID uuid.UUID, jobID, receiptID string, claimID uuid.UUID) (TenantPushAttempt, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return scanTenantPushAttempt(s.db.QueryRowContext(ctx, tenantPushAdmissionSelect, tenantID, jobID, receiptID, claimID), tenantID)
}

func (s *PostgresStore) FinalizeTenantJobDispatch(ctx context.Context, tenantID uuid.UUID, jobID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE publish_jobs SET
			status = CASE WHEN success_count + failure_count + skipped_count + unknown_count = total_count
				THEN 'COMPLETED' ELSE 'DISPATCHED' END,
			completed_at = CASE WHEN success_count + failure_count + skipped_count + unknown_count = total_count
				THEN clock_timestamp() END
		WHERE tenant_id = $1 AND id = $2 AND status IN ('QUEUED', 'IN_PROGRESS')`, tenantID, jobID)
	return err
}

func (s *PostgresStore) FailTenantJobDispatch(ctx context.Context, tenantID uuid.UUID, jobID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var id string
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM publish_jobs
		WHERE tenant_id = $1 AND id = $2 AND status IN ('QUEUED', 'IN_PROGRESS')
		FOR NO KEY UPDATE`, tenantID, jobID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock tenant push job for enqueue failure: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		WITH skipped AS (
			UPDATE delivery_receipts
			SET status = 'SKIPPED', status_reason = $3, dispatched_at = clock_timestamp()
			WHERE tenant_id = $1 AND job_id = $2 AND status = 'PENDING'
			RETURNING id
		)
		UPDATE publish_jobs SET status = 'FAILED', skipped_count = skipped_count + (SELECT count(*) FROM skipped)
		WHERE tenant_id = $1 AND id = $2`, tenantID, jobID, model.TenantPushReasonEnqueueFailed)
	if err != nil {
		return fmt.Errorf("record tenant push enqueue failure: %w", err)
	}
	if err := completeTenantPushJob(ctx, tx, tenantID, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

func completeTenantPushJob(ctx context.Context, tx *sql.Tx, tenantID uuid.UUID, jobID string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE publish_jobs SET completed_at = clock_timestamp(),
			status = CASE WHEN status = 'DISPATCHED' THEN 'COMPLETED' ELSE status END
		WHERE tenant_id = $1 AND id = $2 AND status IN ('DISPATCHED', 'FAILED')
			AND completed_at IS NULL AND success_count + failure_count + skipped_count + unknown_count = total_count`, tenantID, jobID)
	return err
}
