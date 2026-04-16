package workers

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type OutcomeWorker struct {
	store          storage.Store
	dlqPipeline    pipeline.Pipeline[model.SendOutcome]
	queueSize      int
	queueFlushTime int
}

func NewOutcomeWorker(store storage.Store, dlqPipeline pipeline.Pipeline[model.SendOutcome], queueSize int, queueFlushTime int) OutcomeWorker {
	return OutcomeWorker{
		store:          store,
		dlqPipeline:    dlqPipeline,
		queueSize:      queueSize,
		queueFlushTime: queueFlushTime,
	}
}

func (o *OutcomeWorker) Start(ctx context.Context) {
	deliveryCh := make(chan pipeline.Delivery[model.SendOutcome])

	go func() {
		defer close(deliveryCh)
		for {
			d, err := o.dlqPipeline.Receive(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				continue
			}
			deliveryCh <- d
		}
	}()
	outcomeBatch := make([]pipeline.Delivery[model.SendOutcome], 0, o.queueSize)
	ticker := time.NewTicker(time.Duration(o.queueFlushTime) * time.Second)
	flush := func() {
		o.processOutcome(ctx, outcomeBatch)
		clear(outcomeBatch)
		outcomeBatch = outcomeBatch[:0]
	}
	for {
		select {
		case delivery, ok := <-deliveryCh:
			if !ok {
				flush()
				return
			}
			outcomeBatch = append(outcomeBatch, delivery)
			if len(outcomeBatch) == o.queueSize {
				flush()
			}
		case <-ticker.C:
			if len(outcomeBatch) > 0 {
				flush()
			}
		case <-ctx.Done():
			flush()
			return
		}

	}
}

func (o *OutcomeWorker) processOutcome(ctx context.Context, outcomes []pipeline.Delivery[model.SendOutcome]) {
	//TODO: handle retry with maxRetries and retryExp (check apple docs)
	deliveryReceiptBatch := make([]model.DeliveryReceipt, 0)
	for i := range len(outcomes) {
		outcome := outcomes[i].Get()
		deliveryReceiptBatch = append(deliveryReceiptBatch, outcome.Receipt)
	}
	err := o.store.BulkInsertReceipts(ctx, deliveryReceiptBatch)
	if err != nil {
		log.Printf("Error bulk inserting task receipts %v", err.Error())
	}
	return
}
