package workers

import (
	"context"
	"errors"
	"log"

	"github.com/mithileshchellappan/pushboy/internal/pipeline"
)

type SenderWorker[T any, O any] struct {
	taskPipeline pipeline.Pipeline[T]
	dlqPipeline  pipeline.Pipeline[O]
	dispatch     DispatchFunc[T, O]
}

func NewSender[T any, O any](taskPipeline pipeline.Pipeline[T], dlqPipeline pipeline.Pipeline[O], dispatchFunc DispatchFunc[T, O]) SenderWorker[T, O] {
	return SenderWorker[T, O]{
		taskPipeline: taskPipeline,
		dlqPipeline:  dlqPipeline,
		dispatch:     dispatchFunc,
	}
}

func (s *SenderWorker[T, O]) Start(ctx context.Context) {
	for {
		delivery, err := s.taskPipeline.Receive(ctx)

		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, pipeline.ErrClosed) {
				return
			}

			log.Printf("Sender receiver error: %v", err)
			continue
		}

		task := delivery.Get()

		if err := s.dispatch(ctx, task); err != nil {
			log.Printf("Sender dispatch error: %v", err)
		}
		continue
	}

}
