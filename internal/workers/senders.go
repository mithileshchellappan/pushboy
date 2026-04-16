package workers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type SenderWorker struct {
	store        storage.Store
	taskPipeline pipeline.Pipeline[model.SendTask]
	dlqPipeline  pipeline.Pipeline[model.SendOutcome]
	batchSize    int
	dispatchers  map[model.Platform]dispatch.Dispatcher
}

func NewSender(store storage.Store, taskPipeline pipeline.Pipeline[model.SendTask], dlqPipeline pipeline.Pipeline[model.SendOutcome], dispatchers map[model.Platform]dispatch.Dispatcher, batchSize int) SenderWorker {
	return SenderWorker{
		store:        store,
		taskPipeline: taskPipeline,
		dlqPipeline:  dlqPipeline,
		dispatchers:  dispatchers,
		batchSize:    batchSize,
	}
}

func (s *SenderWorker) Start(ctx context.Context) {
	go func() {
		for {
			delivery, err := s.taskPipeline.Receive(ctx)
			log.Printf("received token to send %s", delivery)

			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}

				log.Printf("Sender receiver error: %v", err)
				continue
			}

			s.sendTask(ctx, delivery)
			continue
		}

	}()
}

func (s *SenderWorker) sendTask(ctx context.Context, delivery pipeline.Delivery[model.SendTask]) {

	task := delivery.Get()
	log.Printf("sending to token %s", task.Target.Token)
	err := s.dispatchers[task.Target.Platform].Send(ctx, task.Target.Token, task.Job.Payload)
	if err != nil {
		fmt.Printf("Error sending %s notification, tokenId: %s, error: %v", task.Target.Platform, task.Target.TokenID, err)
		receipt := model.DeliveryReceipt{
			ID:           uuid.New().String(),
			JobID:        task.Job.ID,
			TokenID:      task.Target.TokenID,
			Status:       string(model.Failed),
			StatusReason: err.Error(),
			DispatchedAt: time.Now().UTC().Format(time.RFC3339),
		}

		outcome := model.SendOutcome{
			Receipt: receipt,
			Task:    task,
		}

		err := s.dlqPipeline.Submit(ctx, outcome)

		if errors.Is(err, context.Canceled) {
			return
		}

		if err != nil {
			err2 := s.dlqPipeline.Submit(ctx, outcome) //try resubmitting once
			if err2 != nil {
				fmt.Printf("Error submitting failed job to dlq try 1: %v try 2: %v", err.Error(), err2.Error())
			}
		}
	}
	return
}
