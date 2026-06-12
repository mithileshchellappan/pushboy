package workers

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
)

type pushOutcomeWriter interface {
	ApplyPushOutcomeBatch(ctx context.Context, receipts []model.DeliveryReceipt) error
}

type laOutcomeWriter interface {
	ApplyLAOutcomeBatch(ctx context.Context, outcomes []model.LASendOutcome) error
}

type OutcomeApplyFunc[O any] func(ctx context.Context, outcomes []O) error

type OutcomeWorker[O any] struct {
	dlqPipeline    pipeline.Pipeline[O]
	apply          OutcomeApplyFunc[O]
	queueSize      int
	queueFlushTime int
}

func NewPushOutcomeWorker(store pushOutcomeWriter, dlqPipeline pipeline.Pipeline[model.SendOutcome], queueSize int, queueFlushTime int) OutcomeWorker[model.SendOutcome] {
	return OutcomeWorker[model.SendOutcome]{
		dlqPipeline:    dlqPipeline,
		queueSize:      queueSize,
		queueFlushTime: queueFlushTime,
		apply: func(ctx context.Context, outcomes []model.SendOutcome) error {
			receipts := make([]model.DeliveryReceipt, 0, len(outcomes))
			for _, outcome := range outcomes {
				receipts = append(receipts, outcome.Receipt)
			}
			return store.ApplyPushOutcomeBatch(ctx, receipts)
		},
	}
}

func NewLAOutcomeWorker(store laOutcomeWriter, dlqPipeline pipeline.Pipeline[model.LASendOutcome], queueSize int, queueFlushTime int) OutcomeWorker[model.LASendOutcome] {
	return OutcomeWorker[model.LASendOutcome]{
		dlqPipeline:    dlqPipeline,
		queueSize:      queueSize,
		queueFlushTime: queueFlushTime,
		apply: func(ctx context.Context, outcomes []model.LASendOutcome) error {
			return store.ApplyLAOutcomeBatch(ctx, outcomes)
		},
	}
}

func (o *OutcomeWorker[O]) Start(ctx context.Context) {
	deliveryCh := make(chan pipeline.Delivery[O])

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

	outcomeBatch := make([]pipeline.Delivery[O], 0, o.queueSize)
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

		if err := o.processOutcome(flushCtx, outcomeBatch); err != nil {
			log.Printf("Error processing outcome batch: %v", err)
			return
		}
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
			if len(outcomeBatch) >= o.queueSize {
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

func (o *OutcomeWorker[O]) processOutcome(ctx context.Context, deliveries []pipeline.Delivery[O]) error {
	outcomes := make([]O, 0, len(deliveries))
	for _, delivery := range deliveries {
		outcomes = append(outcomes, delivery.Get())
	}
	return o.apply(ctx, outcomes)
}
