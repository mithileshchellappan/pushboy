package workers

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func FanoutTenantPush(ctx context.Context, store *storage.PostgresStore, batchSize int, job model.JobItem, emit func(context.Context, model.SendTask) error) error {
	if batchSize < 1 {
		return errors.New("tenant push batch size must be positive")
	}
	cursor := ""
	for {
		ids, err := store.GetTenantPushRecipientBatch(ctx, job.TenantID, job.ID, cursor, batchSize)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := emit(ctx, model.SendTask{Job: &job, Target: model.SendTarget{ReceiptID: id}}); err != nil {
				return err
			}
		}
		if len(ids) < batchSize {
			break
		}
		cursor = ids[len(ids)-1]
	}
	return store.FinalizeTenantJobDispatch(ctx, job.TenantID, job.ID)
}

type tenantPushDispatchStore interface {
	ClaimTenantPushRecipient(ctx context.Context, tenantID uuid.UUID, jobID, receiptID string, claimID uuid.UUID) (storage.TenantPushAttempt, error)
	CheckTenantPushRecipient(ctx context.Context, tenantID uuid.UUID, jobID, receiptID string, claimID uuid.UUID) (storage.TenantPushAttempt, error)
	ApplyPushOutcomeBatch(ctx context.Context, receipts []model.DeliveryReceipt) error
}

type tenantPushDispatchRoutes interface {
	SendPush(ctx context.Context, ref model.ProviderRef, platform model.Platform, token string, payload *model.NotificationPayload, admit dispatch.TenantAdmission) error
}

func DispatchTenantPush(ctx context.Context, store tenantPushDispatchStore, routes tenantPushDispatchRoutes, task model.SendTask, outcomes pipeline.Pipeline[model.SendOutcome]) error {
	job := task.Job
	id := task.Target.ReceiptID
	claimID := uuid.New()
	var attempt storage.TenantPushAttempt
	err := retryTenantPushDB(ctx, func(attemptCtx context.Context) error {
		var err error
		attempt, err = store.ClaimTenantPushRecipient(attemptCtx, job.TenantID, job.ID, id, claimID)
		return err
	})
	if errors.Is(err, storage.ErrTenantPushRecipientDone) {
		return nil
	}
	if err != nil && !errors.Is(err, storage.ErrTenantPushRecipientStale) {
		return err
	}
	if err == nil {
		err = routes.SendPush(ctx, attempt.ProviderRef, attempt.Platform, attempt.Token, job.Payload, func(ctx context.Context, ref model.ProviderRef, version int64) error {
			current, e := store.CheckTenantPushRecipient(ctx, job.TenantID, job.ID, id, claimID)
			switch {
			case errors.Is(e, storage.ErrTenantPushRecipientStale), errors.Is(e, storage.ErrTenantPushRecipientDone):
				return model.ErrProviderAdmission
			case e != nil:
				return model.ErrProviderAdmissionCheck
			case ref != current.ProviderRef || version != current.CredentialVersion || attempt.Token != current.Token:
				return model.ErrProviderAdmission
			}
			return nil
		})
		if errors.Is(err, model.ErrProviderRouteUnavailable) {
			if _, e := store.CheckTenantPushRecipient(ctx, job.TenantID, job.ID, id, claimID); errors.Is(e, storage.ErrTenantPushRecipientStale) {
				err = e
			}
		}
	}
	receipt := model.DeliveryReceipt{
		ID: id, TenantID: job.TenantID, JobID: job.ID, ClaimID: claimID,
		DispatchedAt: time.Now().UTC(), Status: model.DeliveryStatusFailed,
	}
	switch {
	case err == nil:
		receipt.Status = model.DeliveryStatusSuccess
	case errors.Is(err, storage.ErrTenantPushRecipientStale), errors.Is(err, model.ErrProviderAdmission):
		receipt.Status = model.DeliveryStatusSkipped
		receipt.StatusReason = model.TenantPushReasonRecipientStale
	case errors.Is(err, model.ErrProviderOutcomeUnknown):
		receipt.Status = model.DeliveryStatusUnknown
		receipt.StatusReason = model.TenantPushReasonDeliveryUnknown
	case errors.Is(err, model.ErrProviderInvalidToken):
		receipt.StatusReason = model.TenantPushReasonInvalidRegistration
		receipt.InvalidateRegistration = true
		var dated *model.TokenInvalidSince
		if errors.As(err, &dated) {
			receipt.TokenInvalidSince = &dated.Time
		}
	case errors.Is(err, model.ErrProviderRouteUnavailable), errors.Is(err, model.ErrProviderAuthorization),
		errors.Is(err, model.ErrProviderAdmissionCheck), errors.Is(err, model.ErrProviderUnavailable),
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		receipt.StatusReason = model.TenantPushReasonProviderUnavailable
	default:
		receipt.StatusReason = model.TenantPushReasonProviderRejected
	}
	submitCtx, cancelSubmit := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	err = outcomes.Submit(submitCtx, model.SendOutcome{Task: task, Receipt: receipt})
	cancelSubmit()
	if err == nil {
		return nil
	}

	persist := func(ctx context.Context) error {
		return store.ApplyPushOutcomeBatch(ctx, []model.DeliveryReceipt{receipt})
	}
	err = retryTenantPushDB(ctx, persist)
	if err == nil || ctx.Err() == nil {
		return err
	}
	flushCtx, cancelFlush := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelFlush()
	return retryTenantPushDB(flushCtx, persist)
}

func retryTenantPushDB(ctx context.Context, operation func(context.Context) error) error {
	for delay := 50 * time.Millisecond; ; delay = min(2*delay, time.Second) {
		if err := ctx.Err(); err != nil {
			return err
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := operation(attemptCtx)
		cancel()
		if !retryableTenantPushDBError(err) {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

func retryableTenantPushDBError(err error) bool {
	if err == nil || errors.Is(err, storage.ErrTenantPushRecipientDone) ||
		errors.Is(err, storage.ErrTenantPushRecipientStale) || errors.Is(err, storage.ErrInvalidTenantResource) {
		return false
	}
	var dbErr *pq.Error
	if errors.As(err, &dbErr) {
		switch dbErr.Code.Class() {
		case "08", "40", "53", "55", "57":
			return true
		default:
			return false
		}
	}
	return true
}
