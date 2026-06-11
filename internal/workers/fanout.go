package workers

import (
	"context"
	"fmt"
	"log"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type FanoutFunc[J any, T any] func(
    ctx context.Context,
    job J,
    emit func(context.Context, T) error,
) error

func FanoutPushToken(ctx context.Context, store storage.Store, batchSize int, job model.JobItem, emit func(context.Context, model.SendTask) error) error {
	store.UpdateJobStatus(ctx, job.ID, model.NotificationJobStatusInProgress)
	cursor := ""
	totalTokenCount := 0
	for {
		var batch *storage.TokenBatch
		var err error
		if job.TopicID != "" {
			batch, err = store.GetTokenBatchForTopic(ctx, job.TopicID, cursor, batchSize)

		} else if job.UserID != "" {
			batch, err = store.GetTokenBatchForUser(ctx, job.UserID, cursor, batchSize)
		} else {
			return fmt.Errorf("can only fetch tokens for either user or topic. Require topicID or userID")
		}
		log.Printf("fetching tokens for job %v", job.ID)
		if err != nil {
			log.Printf("Error fetching tokens: %v", err)
			store.UpdateJobStatus(ctx, job.ID, model.NotificationJobStatusFailed)
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
		  if err := emit(ctx, task); err != nil {
					return err
				}
		}

		if !batch.HasMore {
			break
		}

		cursor = batch.NextCursor
	}
	if err := store.FinalizeJobDispatch(ctx, job.ID, totalTokenCount); err != nil {
		log.Printf("Error finalizing dispatch for job %v: %v", job.ID, err)
		store.UpdateJobStatus(ctx, job.ID, model.NotificationJobStatusFailed)
		return fmt.Errorf("error finalizing dispatch: %v", err)
	}

	if err := store.CompleteJobIfDone(ctx, job.ID); err != nil {
		log.Printf("Error re-checking job completeion after dispatch %v: %v", job.ID, err)
		return fmt.Errorf("error rechechking job completion %v", err)
	}
	return nil
}
