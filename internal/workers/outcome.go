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

type outcomeWriter interface {
	ApplyPushOutcomeBatch(ctx context.Context, receipts []model.DeliveryReceipt) error
	ApplyLAOutcomeBatch(ctx context.Context, outcomes []model.SendOutcome) error
}

type OutcomeWorker struct {
	store          outcomeWriter
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

		retryDeliveries, err := o.processOutcome(flushCtx, outcomeBatch)
		if err != nil {
			log.Printf("Error processing outcome: %v", err)
			clear(outcomeBatch)
			outcomeBatch = retryDeliveries
		} else {
			clear(outcomeBatch)
			outcomeBatch = outcomeBatch[:0]
		}
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

func (o *OutcomeWorker) processOutcome(ctx context.Context, deliveries []pipeline.Delivery[model.SendOutcome]) ([]pipeline.Delivery[model.SendOutcome], error) {
	//TODO: handle retry with maxRetries and retryExp (check apple docs)
	pushOutcomeDeliveries := make([]pipeline.Delivery[model.SendOutcome], 0, len(deliveries))
	pushReceiptBatch := make([]model.DeliveryReceipt, 0, len(deliveries))
	liveActivityOutcomeDeliveries := make([]pipeline.Delivery[model.SendOutcome], 0)
	liveActivityOutcomeBatch := make([]model.SendOutcome, 0)
	softDeleteTokenBatch := make([]string, 0)

	for i := range len(deliveries) {
		delivery := deliveries[i]
		outcome := delivery.Get()
		if outcome.Task.Job.JobType == model.JobTypeLA {
			liveActivityOutcomeDeliveries = append(liveActivityOutcomeDeliveries, delivery)
			liveActivityOutcomeBatch = append(liveActivityOutcomeBatch, outcome)
			continue
		}

		pushOutcomeDeliveries = append(pushOutcomeDeliveries, delivery)
		reason := outcome.Receipt.StatusReason

		if strings.Contains(reason, "BadDeviceToken") {
			softDeleteTokenBatch = append(softDeleteTokenBatch, outcome.Receipt.TokenID)
		}

		pushReceiptBatch = append(pushReceiptBatch, outcome.Receipt)
	}

	retryDeliveries := make([]pipeline.Delivery[model.SendOutcome], 0)
	errs := make([]error, 0, 2)

	if len(pushReceiptBatch) > 0 {
		if err := o.store.ApplyPushOutcomeBatch(ctx, pushReceiptBatch); err != nil {
			log.Printf("Error applying push outcome batch %v", err.Error())
			retryDeliveries = append(retryDeliveries, pushOutcomeDeliveries...)
			errs = append(errs, err)
		}
	}
	if len(softDeleteTokenBatch) > 0 {
		// err := o.store.BulkSoftDeleteToken(ctx, softDeleteTokenBatch) TODO: Bring this back up after fixing android issue
		// if err != nil {
		// 	log.Printf("Error bulk soft deleting dead tokens %v", err.Error())
		// }
	}
	if len(liveActivityOutcomeBatch) > 0 {
		if err := o.store.ApplyLAOutcomeBatch(ctx, liveActivityOutcomeBatch); err != nil {
			log.Printf("Error applying live activity outcome batch %v", err.Error())
			retryDeliveries = append(retryDeliveries, liveActivityOutcomeDeliveries...)
			errs = append(errs, err)
		}
	}

	return retryDeliveries, errors.Join(errs...)
}
