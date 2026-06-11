package workers

import (
	"context"
	"errors"
	// "fmt"
	"log"
	// "time"

	// "github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)



type MasterWorker[J any, T any] struct {
	store        storage.Store
	jobPipeline  pipeline.Pipeline[J]
	taskPipeline pipeline.Pipeline[T]
	fanout FanoutFunc[J, T]
	// workPipeline
}

func NewMaster[J any, T any](store storage.Store, jobPipeline pipeline.Pipeline[J], taskPipeline pipeline.Pipeline[T], fanout FanoutFunc[J, T]) MasterWorker[J, T] {
	return MasterWorker[J,T]{
		store:        store,
		jobPipeline:  jobPipeline,
		taskPipeline: taskPipeline,
		fanout: fanout,
	}
}

func (m *MasterWorker[J,T]) Start(ctx context.Context) {
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

		if err := m.fanout(ctx, job); err != nil {
			log.Printf("Master Fanout error: %v", err)
		}
		continue
	}
}

func (m *MasterWorker[J,T]) Stop() {

}



// func (m *MasterWorker[J,T]) fetchAndPushLATokens(ctx context.Context, job model.JobItem) error {
// 	if err := m.store.UpdateLADispatchStatus(ctx, job.LADispatchID, "IN_PROGRESS"); err != nil {
// 		return fmt.Errorf("error marking live activity dispatch in progress: %w", err)
// 	}

// 	cursor := ""
// 	totalTokenCount := 0
// 	totalFailedOutcomes := make([]model.SendOutcome, 0)
// 	for {
// 		batch, err := m.store.GetLATokenBatchForDispatch(ctx, job.LADispatchID, cursor, m.batchSize)
// 		if err != nil {
// 			_ = m.store.UpdateLADispatchStatus(ctx, job.LADispatchID, "FAILED")
// 			m.failLAStartAfterDispatchFailure(ctx, job)
// 			return fmt.Errorf("error fetching live activity tokens: %w", err)
// 		}

// 		outcomes := m.pushLATokensToPipeline(ctx, batch, &job)
// 		totalFailedOutcomes = append(totalFailedOutcomes, outcomes...)

// 		totalTokenCount += len(batch.Tokens)
// 		if !batch.HasMore {
// 			break
// 		}
// 		cursor = batch.NextCursor
// 	}

// 	if err := m.store.CompleteLADispatchEnqueue(ctx, job.LADispatchID, totalTokenCount); err != nil {
// 		_ = m.store.UpdateLADispatchStatus(ctx, job.LADispatchID, "FAILED")
// 		m.failLAStartAfterDispatchFailure(ctx, job)
// 		return fmt.Errorf("error completing live activity dispatch enqueue: %w", err)
// 	}

// 	if len(totalFailedOutcomes) > 0 {
// 		if err := m.store.ApplyLAOutcomeBatch(ctx, totalFailedOutcomes); err != nil {
// 			return fmt.Errorf("error recording LA enqueue failures: %w", err)
// 		}
// 	}

// 	return nil
// }

// func (m *MasterWorker[J,T]) failLAStartAfterDispatchFailure(ctx context.Context, job model.JobItem) {
// 	if job.LAAction != model.LiveActivityActionStart {
// 		return
// 	}

// 	err := m.store.FailLAJobIfActive(ctx, job.LAJobID)
// 	if err != nil && !errors.Is(err, storage.Errors.NotFound) {
// 		log.Printf("Error failing LA job %s after dispatch failure: %v", job.LAJobID, err)
// 	}
// }

// func (m *MasterWorker[J,T]) pushLATokensToPipeline(ctx context.Context, batch *storage.LiveActivityTokenBatch, job *model.JobItem) []model.SendOutcome {
// 	tokens := batch.Tokens
// 	failedOutcomes := make([]model.SendOutcome, 0)
// 	for _, token := range tokens {
// 		task := model.SendTask{
// 			Target: model.SendTarget{
// 				TokenID:  token.ID,
// 				Token:    token.Token,
// 				Platform: token.Platform,
// 			},
// 			Job: job,
// 		}
// 		if err := m.taskPipeline.Submit(ctx, task); err != nil {
// 			log.Printf("Error adding LA task to pipeline for dispatch %s token %s: %v", job.LADispatchID, token.ID, err)
// 			failedOutcomes = append(failedOutcomes, model.SendOutcome{
// 				Task: task,
// 				Receipt: model.DeliveryReceipt{
// 					JobID:        job.LADispatchID,
// 					TokenID:      token.ID,
// 					Status:       model.DeliveryStatusFailed,
// 					StatusReason: "task enqueue failed",
// 					DispatchedAt: time.Now().UTC(),
// 				},
// 			})
// 		}
// 	}

// 	return failedOutcomes
// }
