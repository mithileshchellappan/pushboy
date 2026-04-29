package workers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type MasterWorker struct {
	store        storage.Store
	jobPipeline  pipeline.Pipeline[model.JobItem]
	taskPipeline pipeline.Pipeline[model.SendTask]
	batchSize    int
	// workPipeline
}

func NewMaster(store storage.Store, jobPipeline pipeline.Pipeline[model.JobItem], taskPipeline pipeline.Pipeline[model.SendTask], batchSize int) MasterWorker {
	return MasterWorker{
		store:        store,
		jobPipeline:  jobPipeline,
		taskPipeline: taskPipeline,
		batchSize:    batchSize,
	}
}

func (m *MasterWorker) Start(ctx context.Context) {
	for {
		delivery, err := m.jobPipeline.Receive(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, pipeline.ErrClosed) {
				return
			}
			log.Printf("Master receive error: %v", err)
			continue
		}

		job := delivery.Get()
		if job.JobType == model.JobTypeLA {
			if err := m.fetchAndPushLATokens(ctx, job); err != nil {
				log.Printf("Error processing LA dispatch %s: %v", job.LADispatchID, err)
			}

			continue
		}

		if err := m.fetchAndPushTokens(ctx, job); err != nil {
			log.Printf("Error processing Push dispatch %s: %v", job.ID, err)

		}
		continue
	}
}

func (m *MasterWorker) Stop() {

}

func (m *MasterWorker) fetchAndPushTokens(ctx context.Context, job model.JobItem) error {
	m.store.UpdateJobStatus(ctx, job.ID, "IN_PROGRESS")
	cursor := ""
	totalTokenCount := 0
	for {
		var batch *storage.TokenBatch
		var err error
		if job.TopicID != "" {
			batch, err = m.store.GetTokenBatchForTopic(ctx, job.TopicID, cursor, m.batchSize)

		} else if job.UserID != "" {
			batch, err = m.store.GetTokenBatchForUser(ctx, job.UserID, cursor, m.batchSize)
		} else {
			return fmt.Errorf("can only fetch tokens for either user or topic. Require topicID or userID")
		}
		log.Printf("fetching tokens for job %v", job.ID)
		if err != nil {
			log.Printf("Error fetching tokens: %v", err)
			m.store.UpdateJobStatus(ctx, job.ID, "FAILED")
			return fmt.Errorf("error fetching tokens: %v", err)
		}
		totalTokenCount += len(batch.Tokens)
		for _, token := range batch.Tokens {
			task := model.SendTask{
				Target: model.SendTarget{
					TokenID:  token.ID,
					Token:    token.Token,
					Platform: token.Platform,
				},
				Job: &job,
			}
			if err := m.taskPipeline.Submit(ctx, task); err != nil {
				fmt.Printf("error adding task to pipeline %v", err)
			}
		}

		if !batch.HasMore {
			break
		}

		cursor = batch.NextCursor
	}
	if err := m.store.FinalizeJobDispatch(ctx, job.ID, totalTokenCount); err != nil {
		log.Printf("Error finalizing dispatch for job %v: %v", job.ID, err)
		m.store.UpdateJobStatus(ctx, job.ID, "FAILED")
		return fmt.Errorf("error finalizing dispatch: %v", err)
	}

	if err := m.store.CompleteJobIfDone(ctx, job.ID); err != nil {
		log.Printf("Error re-checking job completeion after dispatch %v: %v", job.ID, err)
		return fmt.Errorf("error rechechking job completion %v", err)
	}
	return nil
}

func (m *MasterWorker) fetchAndPushLATokens(ctx context.Context, job model.JobItem) error {
	if err := m.store.UpdateLADispatchStatus(ctx, job.LADispatchID, "IN_PROGRESS"); err != nil {
		return fmt.Errorf("error marking live activity dispatch in progress: %w", err)
	}

	cursor := ""
	totalTokenCount := 0
	totalFailedOutcomes := make([]model.SendOutcome, 0)
	for {
		batch, err := m.store.GetLATokenBatchForDispatch(ctx, job.LADispatchID, cursor, m.batchSize)
		if err != nil {
			_ = m.store.UpdateLADispatchStatus(ctx, job.LADispatchID, "FAILED")
			m.failLAStartAfterDispatchFailure(ctx, job)
			return fmt.Errorf("error fetching live activity tokens: %w", err)
		}

		outcomes := m.pushLATokensToPipeline(ctx, batch, &job)
		totalFailedOutcomes = append(totalFailedOutcomes, outcomes...)

		totalTokenCount += len(batch.Tokens)
		if !batch.HasMore {
			break
		}
		cursor = batch.NextCursor
	}

	if err := m.store.CompleteLADispatchEnqueue(ctx, job.LADispatchID, totalTokenCount); err != nil {
		_ = m.store.UpdateLADispatchStatus(ctx, job.LADispatchID, "FAILED")
		m.failLAStartAfterDispatchFailure(ctx, job)
		return fmt.Errorf("error completing live activity dispatch enqueue: %w", err)
	}

	if len(totalFailedOutcomes) > 0 {
		if err := m.store.ApplyLAOutcomeBatch(ctx, totalFailedOutcomes); err != nil {
			return fmt.Errorf("error recording LA enqueue failures: %w", err)
		}
	}

	return nil
}

func (m *MasterWorker) failLAStartAfterDispatchFailure(ctx context.Context, job model.JobItem) {
	if job.LAAction != model.LiveActivityActionStart {
		return
	}

	err := m.store.FailLAJobIfActive(ctx, job.LAJobID)
	if err != nil && !errors.Is(err, storage.Errors.NotFound) {
		log.Printf("Error failing LA job %s after dispatch failure: %v", job.LAJobID, err)
	}
}

func (m *MasterWorker) pushLATokensToPipeline(ctx context.Context, batch *storage.LiveActivityTokenBatch, job *model.JobItem) []model.SendOutcome {
	tokens := batch.Tokens
	failedOutcomes := make([]model.SendOutcome, 0)
	for _, token := range tokens {
		task := model.SendTask{
			Target: model.SendTarget{
				TokenID:  token.ID,
				Token:    token.Token,
				Platform: token.Platform,
			},
			Job: job,
		}
		if err := m.taskPipeline.Submit(ctx, task); err != nil {
			log.Printf("Error adding LA task to pipeline for dispatch %s token %s: %v", job.LADispatchID, token.ID, err)
			failedOutcomes = append(failedOutcomes, model.SendOutcome{
				Task: task,
				Receipt: model.DeliveryReceipt{
					JobID:        job.LADispatchID,
					TokenID:      token.ID,
					Status:       string(model.Failed),
					StatusReason: "task enqueue failed",
					DispatchedAt: time.Now().UTC().Format(time.RFC3339),
				},
			})
		}
	}

	return failedOutcomes
}
