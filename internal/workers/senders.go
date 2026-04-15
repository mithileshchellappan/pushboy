package workers

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type SenderWorker struct {
	store        storage.Store
	taskPipeline pipeline.Pipeline[model.SendTask]
	batchSize    int
	dispatchers  map[model.Platform]dispatch.Dispatcher
}

func NewSender(store storage.Store, taskPipeline pipeline.Pipeline[model.SendTask], dispatchers map[model.Platform]dispatch.Dispatcher, batchSize int) SenderWorker {
	return SenderWorker{
		store:        store,
		taskPipeline: taskPipeline,
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

			s.SendTask(ctx, delivery)
			continue
		}

	}()
}

func (s *SenderWorker) SendTask(ctx context.Context, delivery pipeline.Delivery[model.SendTask]) {

	task := delivery.Get()
	log.Printf("sending to token %s", task.Target.Token)
	err := s.dispatchers[task.Target.Platform].Send(ctx, task.Target.Token, task.Job.Payload)
	if err != nil {
		fmt.Printf("Error sending %s notification, tokenId: %s, error: %v", task.Target.Platform, task.Target.TokenID, err)

	}
	fmt.Printf("Successfully send notification %s", task.Target.Platform)

}
