package workers

import (
	"context"
	"errors"
	"log"
	"strings"
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
				if errors.Is(err, context.Canceled) || errors.Is(err, pipeline.ErrClosed) {
					return
				}
				log.Printf("Outcome receive error: %v", err)
				continue
			}
			select {
			case deliveryCh <- d:
			case <-ctx.Done():
				return
			}
		}
	}()
	outcomeBatch := make([]pipeline.Delivery[model.SendOutcome], 0, o.queueSize)
	ticker := time.NewTicker(time.Duration(o.queueFlushTime) * time.Second)
	defer ticker.Stop()
	flush := func() {
		if len(outcomeBatch) == 0 {
			return
		}

		flushCtx := ctx
		cancel := func() {}
		if ctx.Err() != nil {
			flushCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		}
		defer cancel()

		o.processOutcome(flushCtx, outcomeBatch)
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
	deliveryReceiptBatch := make([]model.DeliveryReceipt, 0, len(outcomes))
	softDeleteTokenBatch := make([]string, 0)
	for i := range len(outcomes) {
		outcome := outcomes[i].Get()
		reason := outcome.Receipt.StatusReason

		if strings.Contains(reason, "BadDeviceToken") {
			softDeleteTokenBatch = append(softDeleteTokenBatch, outcome.Receipt.TokenID)
		}

		deliveryReceiptBatch = append(deliveryReceiptBatch, outcome.Receipt)
	}
	err := o.store.BulkInsertReceipts(ctx, deliveryReceiptBatch)

	if err != nil {
		log.Printf("Error bulk inserting task receipts %v", err.Error())
	}
	if len(softDeleteTokenBatch) > 0 {
		err := o.store.BulkSoftDeleteToken(ctx, softDeleteTokenBatch)
		if err != nil {
			log.Printf("Error bulk soft deleting dead tokens %v", err.Error())
		}
	}
	return
}
