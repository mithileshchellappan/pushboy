package pipeline

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type NotificationPipeline struct {
	store       storage.Store
	dispatchers map[string]dispatch.Dispatcher
	tokensChan  chan storage.Token
	resultsChan chan storage.DeliveryReceipt
	numSenders  int
	batchSize   int
}

func NewNotificationPipeline(store storage.Store, dispatchers map[string]dispatch.Dispatcher, numSenders int, batchSize int) *NotificationPipeline {
	return &NotificationPipeline{
		store:       store,
		dispatchers: dispatchers,
		tokensChan:  make(chan storage.Token, batchSize),
		resultsChan: make(chan storage.DeliveryReceipt, batchSize),
		numSenders:  numSenders,
		batchSize:   batchSize,
	}
}

func (p *NotificationPipeline) ProcessJob(ctx context.Context, job *storage.PublishJob) error {
	log.Printf("WORKER: -> Starting job: %s", job.ID)
	var wg sync.WaitGroup

	p.store.UpdateJobStatus(ctx, job.ID, "IN_PROGRESS")
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.collectResults(ctx)
	}()
	go p.fetchTokens(ctx, job)

	p.startSenders(ctx, job)
	wg.Wait()
	p.store.UpdateJobStatus(ctx, job.ID, "COMPLETED")

	return nil
}

func (p *NotificationPipeline) fetchTokens(ctx context.Context, job *storage.PublishJob) error {
	defer close(p.tokensChan)
	cursor := ""

	for {
		var batch *storage.TokenBatch
		var err error
		if job.TopicID != "" {
			batch, err = p.store.GetTokenBatchForTopic(ctx, job.TopicID, cursor, p.batchSize)

		} else if job.UserID != "" {
			batch, err = p.store.GetTokenBatchForUser(ctx, job.UserID, cursor, p.batchSize)
		} else {
			return fmt.Errorf("can only fetch tokens for either user or topic. Require topicID or userID")
		}

		if err != nil {
			log.Printf("Error fetching tokens: %v", err)
			return fmt.Errorf("error fetching tokens: %v", err)
		}

		for _, token := range batch.Tokens {
			select {
			case p.tokensChan <- token:
			case <-ctx.Done():
				return nil
			}
		}

		if !batch.HasMore {
			break
		}

		cursor = batch.NextCursor
	}
	return nil
}

func (p *NotificationPipeline) startSenders(ctx context.Context, job *storage.PublishJob) {
	var wg sync.WaitGroup
	for i := 1; i <= p.numSenders; i++ {
		wg.Add(1)
		go p.sender(ctx, job, &wg)
	}
	wg.Wait()
	close(p.resultsChan)
}

func (p *NotificationPipeline) sender(ctx context.Context, job *storage.PublishJob, wg *sync.WaitGroup) {
	defer wg.Done()

	// Convert storage.NotificationPayload to dispatch.NotificationPayload
	var payload *dispatch.NotificationPayload
	if job.Payload != nil {
		payload = &dispatch.NotificationPayload{
			Title:      job.Payload.Title,
			Body:       job.Payload.Body,
			ImageURL:   job.Payload.ImageURL,
			Sound:      job.Payload.Sound,
			Badge:      job.Payload.Badge,
			Data:       job.Payload.Data,
			Silent:     job.Payload.Silent,
			CollapseID: job.Payload.CollapseID,
			Priority:   job.Payload.Priority,
			TTL:        job.Payload.TTL,
			ThreadID:   job.Payload.ThreadID,
			Category:   job.Payload.Category,
		}
	} else {
		// Fallback for jobs without payload (shouldn't happen but safe)
		payload = &dispatch.NotificationPayload{}
	}

	for token := range p.tokensChan {
		dispatcher, ok := p.dispatchers[token.Platform]
		deliveryReceipt := storage.DeliveryReceipt{
			ID:           uuid.New().String(),
			JobID:        job.ID,
			TokenID:      token.ID,
			Status:       "SUCCESS",
			StatusReason: "",
			DispatchedAt: time.Now().UTC().Format(time.RFC3339),
		}

		if !ok {
			deliveryReceipt.Status = "FAILED"
			deliveryReceipt.StatusReason = fmt.Sprintf("Unknown dispatcher platform: %s", token.Platform)
			p.resultsChan <- deliveryReceipt
			continue
		}

		err := dispatcher.Send(ctx, &token, payload)

		if err != nil {
			deliveryReceipt.Status = "FAILED"
			deliveryReceipt.StatusReason = err.Error()
		}
		p.resultsChan <- deliveryReceipt
	}
}

func (p *NotificationPipeline) collectResults(ctx context.Context) {
	var batch []storage.DeliveryReceipt

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}

		if err := p.store.BulkInsertReceipts(ctx, batch); err != nil {
			log.Printf("WORKER: -> CRITICAL: failed to bulk insert delivery receipts: %v", err)
		}

		batch = batch[:0]
	}

	for {
		select {
		case receipt, ok := <-p.resultsChan:

			if !ok {
				flush()
				return
			}

			batch = append(batch, receipt)
			if len(batch) >= 1000 {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-ctx.Done():
			flush()
			return
		}
	}
}
