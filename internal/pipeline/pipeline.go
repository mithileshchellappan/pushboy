// package pipeline

// import (
// 	"github.com/mithileshchellappan/pushboy/internal/model"
// 	"github.com/mithileshchellappan/pushboy/internal/storage"
// )

// type JobPipeline struct {
// 	store   storage.Store
// 	jobChan chan model.JobItem
// }

// func NewJobPipeline(store storage.Store) *JobPipeline {
// 	return &JobPipeline{
// 		store:   store,
// 		jobChan: make(chan model.JobItem),
// 	}
// }

// func (p *JobPipeline) SubmitJob(job *model.JobItem) {
// 	p.jobChan <- *job
// }

// func (p *JobPipeline) Close() {
// 	close(p.jobChan)
// }

// func (p *JobPipeline) Jobs() <-chan model.JobItem {
// 	return p.jobChan
// }

package pipeline

import (
	"context"
	"errors"
)

var ErrClosed = errors.New("pipeline closed")

type Delivery[T any] interface {
	Get() T
	Retry(ctx context.Context, maxRetry int) error
}

type Pipeline[T any] interface {
	Submit(ctx context.Context, item T) error
	Receive(ctx context.Context) (Delivery[T], error)
	Close(ctx context.Context) error
}

// func (p *JobPipeline) fetchTokens(ctx context.Context, job *model.JobItem) error {
// 	defer close(p.tokensChan)
// 	cursor := ""

// 	for {
// 		var batch *storage.TokenBatch
// 		var err error
// 		if job.TopicID != "" {
// 			batch, err = p.store.GetTokenBatchForTopic(ctx, job.TopicID, cursor, p.batchSize)

// 		} else if job.UserID != "" {
// 			batch, err = p.store.GetTokenBatchForUser(ctx, job.UserID, cursor, p.batchSize)
// 		} else {
// 			return fmt.Errorf("can only fetch tokens for either user or topic. Require topicID or userID")
// 		}

// 		if err != nil {
// 			log.Printf("Error fetching tokens: %v", err)
// 			return fmt.Errorf("error fetching tokens: %v", err)
// 		}

// 		for _, token := range batch.Tokens {
// 			select {
// 			case p.tokensChan <- token:
// 			case <-ctx.Done():
// 				return nil
// 			}
// 		}

// 		if !batch.HasMore {
// 			break
// 		}

// 		cursor = batch.NextCursor
// 	}
// 	return nil
// }

// func (p *JobPipeline) startSenders(ctx context.Context, job *storage.PublishJob) {
// 	var wg sync.WaitGroup
// 	for i := 1; i <= p.numSenders; i++ {
// 		wg.Add(1)
// 		go p.sender(ctx, job, &wg)
// 	}
// 	wg.Wait()
// 	close(p.resultsChan)
// }

// func (p *JobPipeline) sender(ctx context.Context, job *storage.PublishJob, wg *sync.WaitGroup) {
// 	defer wg.Done()

// 	// Convert storage.NotificationPayload to dispatch.NotificationPayload
// 	var payload *model.NotificationPayload
// 	if job.Payload != nil {
// 		payload = &model.NotificationPayload{
// 			Title:      job.Payload.Title,
// 			Body:       job.Payload.Body,
// 			ImageURL:   job.Payload.ImageURL,
// 			Sound:      job.Payload.Sound,
// 			Badge:      job.Payload.Badge,
// 			Data:       job.Payload.Data,
// 			Silent:     job.Payload.Silent,
// 			CollapseID: job.Payload.CollapseID,
// 			Priority:   job.Payload.Priority,
// 			TTL:        job.Payload.TTL,
// 			ThreadID:   job.Payload.ThreadID,
// 			Category:   job.Payload.Category,
// 		}
// 	} else {
// 		// Fallback for jobs without payload (shouldn't happen but safe)
// 		payload = &model.NotificationPayload{}
// 	}

// 	for token := range p.tokensChan {
// 		dispatcher, ok := p.dispatchers[token.Platform]
// 		deliveryReceipt := storage.DeliveryReceipt{
// 			ID:           uuid.New().String(),
// 			JobID:        job.ID,
// 			TokenID:      token.ID,
// 			Status:       "SUCCESS",
// 			StatusReason: "",
// 			DispatchedAt: time.Now().UTC().Format(time.RFC3339),
// 		}

// 		if !ok {
// 			deliveryReceipt.Status = "FAILED"
// 			deliveryReceipt.StatusReason = fmt.Sprintf("Unknown dispatcher platform: %s", token.Platform)
// 			p.resultsChan <- deliveryReceipt
// 			continue
// 		}

// 		err := dispatcher.Send(ctx, token.Token, payload)

// 		if err != nil {
// 			deliveryReceipt.Status = "FAILED"
// 			deliveryReceipt.StatusReason = err.Error()
// 		}
// 		p.resultsChan <- deliveryReceipt
// 	}
// }
