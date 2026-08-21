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

type laFanoutStore interface {
	UpdateLADispatchStatus(context.Context, string, string) error
	SupersedeLADispatchIfStale(context.Context, string, int) (bool, error)
	NewLATokenPager(context.Context, string) (storage.LiveActivityTokenPager, error)
	CompleteLADispatchEnqueue(context.Context, string, int) error
	FailLADispatchEnqueue(context.Context, string, int) error
	ApplyLAOutcomeBatch(context.Context, []model.LASendOutcome) error
	FailLAJobIfActive(context.Context, string) error
}

func FanoutLATokens(ctx context.Context, store laFanoutStore, job model.LAJobItem, batchSize int, emit func(context.Context, model.LASendTask) error) error {
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
	totalTargetCount := 0
	totalFailedOutcomes := make([]model.LASendOutcome, 0)
	channelPending := job.ChannelID != "" &&
		job.Action != model.LiveActivityActionStart
	var tokenPager storage.LiveActivityTokenPager
	for {
		if job.Action == model.LiveActivityActionUpdate {
			superseded, err := store.SupersedeLADispatchIfStale(ctx, job.DispatchID, totalTargetCount)
			if err != nil {
				return failLADispatchFanout(
					ctx,
					store,
					job,
					totalTargetCount,
					totalFailedOutcomes,
					fmt.Errorf("error checking supersede state for dispatch %s after %d emitted tasks: %w", job.DispatchID, totalTargetCount, err),
				)
			}
			if superseded {
				log.Printf("LA dispatch %s superseded by newer update after %d emitted tasks, skipping rest", job.DispatchID, totalTargetCount)
				return applyLAEnqueueFailures(ctx, store, totalFailedOutcomes)
			}
		}

		if channelPending {
			channelTask := model.LASendTask{
				Target: model.SendTarget{
					Platform: model.APNS,
				},
				LAJob:     &job,
				ChannelID: job.ChannelID,
			}
			totalTargetCount++
			channelPending = false
			if err := emit(ctx, channelTask); err != nil {
				totalFailedOutcomes = append(totalFailedOutcomes, model.LASendOutcome{
					Task: channelTask,
					Receipt: model.DeliveryReceipt{
						JobID:        job.DispatchID,
						Status:       model.DeliveryStatusFailed,
						StatusReason: "channel task enqueue failed",
						DispatchedAt: time.Now().UTC(),
					},
				})
			}
		}

		if tokenPager == nil {
			pager, err := store.NewLATokenPager(ctx, job.DispatchID)
			if err != nil {
				return failLADispatchFanout(ctx, store, job, totalTargetCount, totalFailedOutcomes, fmt.Errorf("error creating live activity token pager: %w", err))
			}
			if pager == nil {
				return failLADispatchFanout(ctx, store, job, totalTargetCount, totalFailedOutcomes, errors.New("live activity token pager is unavailable"))
			}
			tokenPager = pager
		}

		batch, err := tokenPager.Next(ctx, cursor, batchSize)
		if err != nil {
			return failLADispatchFanout(ctx, store, job, totalTargetCount, totalFailedOutcomes, fmt.Errorf("error fetching live activity tokens: %w", err))
		}

		outcomes := pushLATokensToPipeline(ctx, batch, emit, &job)
		totalFailedOutcomes = append(totalFailedOutcomes, outcomes...)

		totalTargetCount += len(batch.Tokens)
		if !batch.HasMore {
			break
		}
		cursor = batch.NextCursor
	}

	if err := store.CompleteLADispatchEnqueue(ctx, job.DispatchID, totalTargetCount); err != nil {
		return failLADispatchFanout(ctx, store, job, totalTargetCount, totalFailedOutcomes, fmt.Errorf("error completing live activity dispatch enqueue: %w", err))
	}

	return applyLAEnqueueFailures(ctx, store, totalFailedOutcomes)
}

func failLADispatchFanout(
	ctx context.Context,
	store laFanoutStore,
	job model.LAJobItem,
	totalTargetCount int,
	failedOutcomes []model.LASendOutcome,
	cause error,
) error {
	errs := []error{cause}
	if err := store.FailLADispatchEnqueue(ctx, job.DispatchID, totalTargetCount); err != nil {
		errs = append(errs, err)
	}
	if err := applyLAEnqueueFailures(ctx, store, failedOutcomes); err != nil {
		errs = append(errs, err)
	}
	failLAStartAfterDispatchFailure(ctx, store, job)
	return errors.Join(errs...)
}

func applyLAEnqueueFailures(ctx context.Context, store laFanoutStore, failedOutcomes []model.LASendOutcome) error {
	if len(failedOutcomes) == 0 {
		return nil
	}
	if err := store.ApplyLAOutcomeBatch(ctx, failedOutcomes); err != nil {
		return fmt.Errorf("error recording LA enqueue failures: %w", err)
	}
	return nil
}

func failLAStartAfterDispatchFailure(ctx context.Context, store laFanoutStore, job model.LAJobItem) {
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

			SupportsBroadcastChannels: token.SupportsBroadcastChannels,
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
