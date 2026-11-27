package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/mithileshchellappan/pushboy/internal/model"
)

func (s *PostgresStore) applyTenantPushOutcome(ctx context.Context, receipt model.DeliveryReceipt) error {
	status, reason := tenantPushReceiptResult(receipt)
	if status == "" || receipt.ClaimID == uuid.Nil {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var locked int
	err = tx.QueryRowContext(ctx, `
		SELECT 1 FROM publish_jobs WHERE tenant_id = $1 AND id = $2 FOR NO KEY UPDATE`,
		receipt.TenantID, receipt.JobID).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock tenant push job for outcome: %w", err)
	}
	var tokenID string
	err = tx.QueryRowContext(ctx, `
		SELECT token_id FROM delivery_receipts
		WHERE tenant_id = $1 AND job_id = $2 AND id = $3 AND claim_id = $4 AND status = 'SENDING'`,
		receipt.TenantID, receipt.JobID, receipt.ID, receipt.ClaimID).Scan(&tokenID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read claimed tenant push receipt: %w", err)
	}

	if receipt.InvalidateRegistration && reason == model.TenantPushReasonInvalidRegistration {
		current, err := readTenantPushRecipient(ctx, tx, receipt.TenantID, receipt.JobID, receipt.ID, receipt.ClaimID, true)
		if err != nil && !errors.Is(err, ErrTenantPushRecipientStale) {
			return err
		}
		if err == nil {
			if _, err := tx.ExecContext(ctx, `
				UPDATE tokens SET is_deleted = TRUE
				WHERE tenant_id = $1 AND id = $2 AND provider_config_id = $3`,
				receipt.TenantID, tokenID, current.ProviderRef.RouteID); err != nil {
				return fmt.Errorf("invalidate tenant push registration: %w", err)
			}
		}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE delivery_receipts SET status = $5, status_reason = NULLIF($6, ''), dispatched_at = $7
		WHERE tenant_id = $1 AND job_id = $2 AND id = $3 AND claim_id = $4 AND status = 'SENDING'`,
		receipt.TenantID, receipt.JobID, receipt.ID, receipt.ClaimID, status, reason, requiredTime(receipt.DispatchedAt)); err != nil {
		return fmt.Errorf("record tenant push outcome: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE publish_jobs SET
			success_count = success_count + ($3 = 'SUCCESS')::integer,
			failure_count = failure_count + ($3 = 'FAILED')::integer,
			skipped_count = skipped_count + ($3 = 'SKIPPED')::integer,
			unknown_count = unknown_count + ($3 = 'UNKNOWN')::integer
		WHERE tenant_id = $1 AND id = $2`, receipt.TenantID, receipt.JobID, status); err != nil {
		return fmt.Errorf("count tenant push outcome: %w", err)
	}
	if err := completeTenantPushJob(ctx, tx, receipt.TenantID, receipt.JobID); err != nil {
		return fmt.Errorf("complete tenant push job: %w", err)
	}
	return tx.Commit()
}

func tenantPushReceiptResult(receipt model.DeliveryReceipt) (model.DeliveryStatus, string) {
	switch receipt.Status {
	case model.DeliveryStatusSuccess:
		return receipt.Status, ""
	case model.DeliveryStatusSkipped:
		return receipt.Status, model.TenantPushReasonRecipientStale
	case model.DeliveryStatusUnknown:
		return receipt.Status, model.TenantPushReasonDeliveryUnknown
	case model.DeliveryStatusFailed:
		switch receipt.StatusReason {
		case model.TenantPushReasonInvalidRegistration, model.TenantPushReasonProviderUnavailable:
			return receipt.Status, receipt.StatusReason
		default:
			return receipt.Status, model.TenantPushReasonProviderRejected
		}
	default:
		return "", ""
	}
}
