package workers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func FanoutLATokens(ctx context.Context, store storage.Store, job model.LAJobItem, batchSize int, emit func(context.Context, model.LASendTask) error) error {
	if job.Action == model.LiveActivityActionUpdate {
		superseded, err := store.SupersedeLADispatchIfStale(ctx, job.DispatchID, 0)
		if err != nil {
			return failLADispatchFanout(
				ctx,
				store,
				job,
				0,
				nil,
				fmt.Errorf("error checking supersede state for dispatch %s: %w", job.DispatchID, err),
			)
		}
		if superseded {
			log.Printf("LA dispatch %s superseded by newer update, skipping", job.DispatchID)
			return nil
		}
	}

	if err := store.UpdateLADispatchStatus(ctx, job.DispatchID, "IN_PROGRESS"); err != nil {
		return failLADispatchFanout(ctx, store, job, 0, nil, fmt.Errorf("error marking live activity dispatch in progress: %w", err))
	}

	cursor := ""
	totalTokenCount := 0
	totalFailedOutcomes := make([]model.LASendOutcome, 0)
	for {
		if job.Action == model.LiveActivityActionUpdate {
			superseded, err := store.SupersedeLADispatchIfStale(ctx, job.DispatchID, totalTokenCount)
			if err != nil {
				return failLADispatchFanout(
					ctx,
					store,
					job,
					totalTokenCount,
					totalFailedOutcomes,
					fmt.Errorf("error checking supersede state for dispatch %s after %d emitted tasks: %w", job.DispatchID, totalTokenCount, err),
				)
			}
			if superseded {
				log.Printf("LA dispatch %s superseded by newer update after %d emitted tasks, skipping rest", job.DispatchID, totalTokenCount)
				return applyLAEnqueueFailures(ctx, store, totalFailedOutcomes)
			}
		}
		batch, err := store.GetLATokenBatchForDispatch(ctx, job.DispatchID, cursor, batchSize)
		if err != nil {
			return failLADispatchFanout(ctx, store, job, totalTokenCount, totalFailedOutcomes, fmt.Errorf("error fetching live activity tokens: %w", err))
		}

		outcomes := pushLATokensToPipeline(ctx, batch, emit, &job)
		totalFailedOutcomes = append(totalFailedOutcomes, outcomes...)

		totalTokenCount += len(batch.Tokens)
		if !batch.HasMore {
			break
		}
		cursor = batch.NextCursor
	}

	if err := store.CompleteLADispatchEnqueue(ctx, job.DispatchID, totalTokenCount); err != nil {
		return failLADispatchFanout(ctx, store, job, totalTokenCount, totalFailedOutcomes, fmt.Errorf("error completing live activity dispatch enqueue: %w", err))
	}

	return applyLAEnqueueFailures(ctx, store, totalFailedOutcomes)
}

func failLADispatchFanout(
	ctx context.Context,
	store storage.Store,
	job model.LAJobItem,
	totalTokenCount int,
	failedOutcomes []model.LASendOutcome,
	cause error,
) error {
	errs := []error{cause}
	if err := store.FailLADispatchEnqueue(ctx, job.DispatchID, totalTokenCount); err != nil {
		errs = append(errs, err)
	}
	if err := applyLAEnqueueFailures(ctx, store, failedOutcomes); err != nil {
		errs = append(errs, err)
	}
	failLAStartAfterDispatchFailure(ctx, store, job)
	return errors.Join(errs...)
}

func applyLAEnqueueFailures(ctx context.Context, store storage.Store, failedOutcomes []model.LASendOutcome) error {
	if len(failedOutcomes) == 0 {
		return nil
	}
	if err := store.ApplyLAOutcomeBatch(ctx, failedOutcomes); err != nil {
		return fmt.Errorf("error recording LA enqueue failures: %w", err)
	}
	return nil
}

func failLAStartAfterDispatchFailure(ctx context.Context, store storage.Store, job model.LAJobItem) {
	if job.Action != model.LiveActivityActionStart {
		return
	}

	err := store.FailLAJobIfActive(ctx, job.JobID)
	if err != nil && !errors.Is(err, storage.Errors.NotFound) {
		log.Printf("Error failing LA job %s after dispatch failure: %v", job.JobID, err)
	}
}

func pushLATokensToPipeline(ctx context.Context, batch *storage.LiveActivityTokenBatch, emit func(context.Context, model.LASendTask) error, job *model.LAJobItem) []model.LASendOutcome {
	tokens := batch.Tokens
	failedOutcomes := make([]model.LASendOutcome, 0)
	for _, token := range tokens {
		task := model.LASendTask{
			Target: model.SendTarget{
				TokenID:  token.ID,
				Token:    token.Token,
				Platform: token.Platform,
			},
			LAJob: job,
		}
		if err := emit(ctx, task); err != nil {
			log.Printf("Error adding LA task to pipeline for dispatch %s token %s: %v", job.DispatchID, token.ID, err)
			failedOutcomes = append(failedOutcomes, model.LASendOutcome{
				Task: task,
				Receipt: model.DeliveryReceipt{
					JobID:        job.DispatchID,
					TokenID:      token.ID,
					Status:       model.DeliveryStatusFailed,
					StatusReason: "task enqueue failed",
					DispatchedAt: time.Now().UTC(),
				},
			})
		}
	}

	return failedOutcomes
}
