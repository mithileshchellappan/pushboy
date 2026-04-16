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
	for {
		delivery, err := s.taskPipeline.Receive(ctx)

		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, pipeline.ErrClosed) {
				return
			}

			log.Printf("Sender receiver error: %v", err)
			continue
		}

		s.sendTask(ctx, delivery)
		continue
	}

}

func (s *SenderWorker) sendTask(ctx context.Context, delivery pipeline.Delivery[model.SendTask]) {

	task := delivery.Get()
	log.Printf("sending to token %s", task.Target.Token)

	dispatcher, ok := s.dispatchers[task.Target.Platform]
	receipt := model.DeliveryReceipt{
		ID:           uuid.New().String(),
		JobID:        task.Job.ID,
		TokenID:      task.Target.TokenID,
		DispatchedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if !ok {
		receipt.Status = string(model.Failed)
		receipt.StatusReason = fmt.Sprintf("Unknown dispatcher platform: %s", task.Target.Platform)
		s.pushToDLQ(ctx, receipt, task)
		return
	}

	err := dispatcher.Send(ctx, task.Target.Token, task.Job.Payload)
	if err != nil {
		fmt.Printf("Error sending %s notification, tokenId: %s, error: %v", task.Target.Platform, task.Target.TokenID, err)
		receipt.Status = string(model.Failed)
		receipt.StatusReason = err.Error()
		s.pushToDLQ(ctx, receipt, task)
		return
	}
	receipt.Status = string(model.Success)
	s.pushToDLQ(ctx, receipt, task)
	return
}

func (s *SenderWorker) pushToDLQ(ctx context.Context, receipt model.DeliveryReceipt, task model.SendTask) {
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
	return
}
