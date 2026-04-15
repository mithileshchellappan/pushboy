package workers

import (
	"context"
	"errors"
	"fmt"
	"log"

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
	go func() {
		for {
			delivery, err := m.jobPipeline.Receive(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}

				log.Printf("Master receive error: %v", err)
				continue
			}
			m.fetchAndPushTokens(ctx, delivery)
			continue
		}
	}()
}

func (m *MasterWorker) Stop() {

}

func (m *MasterWorker) fetchAndPushTokens(ctx context.Context, delivery pipeline.Delivery[model.JobItem]) error {
	// defer close(p.tokensChan)
	cursor := ""

	for {
		var batch *storage.TokenBatch
		var err error
		job := delivery.Get()
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
			return fmt.Errorf("error fetching tokens: %v", err)
		}
		for _, token := range batch.Tokens {
			task := model.SendTask{
				Target: model.SendTarget{
					TokenID:  token.ID,
					Token:    token.Token,
					Platform: token.Platform,
				},
				Job: &job,
			}
			err := m.taskPipeline.Submit(ctx, task)
			if err != nil {
				fmt.Printf("error adding task to pipeline %v", err)
			}
		}

		if !batch.HasMore {
			break
		}

		cursor = batch.NextCursor
	}
	return nil
}
