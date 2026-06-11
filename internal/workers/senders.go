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
	dispatch DispatchFunc[T, O]
}

func NewSender[T any, O any]( taskPipeline pipeline.Pipeline[T], dlqPipeline pipeline.Pipeline[O], dispatchFunc DispatchFunc[T, O] ) SenderWorker[T,O] {
	return SenderWorker[T, O]{
		taskPipeline: taskPipeline,
		dlqPipeline:  dlqPipeline,
		dispatch: dispatchFunc,
	}
}

func (s *SenderWorker[T,O]) Start(ctx context.Context) {
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



// func (s *SenderWorker) sendLATask(ctx context.Context, task model.SendTask) {
// 	dispatcher, ok := s.dispatchers[task.Target.Platform]
// 	receipt := model.DeliveryReceipt{
// 		ID:           uuid.New().String(),
// 		JobID:        task.Job.ID,
// 		TokenID:      task.Target.TokenID,
// 		DispatchedAt: time.Now().UTC(),
// 	}
// 	if !ok {
// 		receipt.Status = model.DeliveryStatusFailed
// 		receipt.StatusReason = fmt.Sprintf("Unknown dispatcher platform: %s", task.Target.Platform)
// 		s.pushToDLQ(ctx, receipt, task)
// 		return
// 	}

// 	laDispatcher, ok := dispatcher.(dispatch.LiveActivityDispatcher)
// 	if !ok {
// 		receipt.Status = model.DeliveryStatusFailed
// 		receipt.StatusReason = fmt.Sprintf("No live activity dispatcher configured for platform: %s", task.Target.Platform)
// 		s.pushToDLQ(ctx, receipt, task)
// 		return
// 	}

// 	err := laDispatcher.SendLiveActivity(ctx, task.Target.Token, &model.LiveActivityRequest{
// 		Action:       task.Job.LAAction,
// 		ActivityID:   task.Job.LAActivityID,
// 		ActivityType: task.Job.LAActivity,
// 		Payload:      task.Job.LAPayload,
// 		Options:      task.Job.LAOptions,
// 	})
// 	if err != nil {
// 		fmt.Printf("Error sending %s live activity, tokenId: %s, error: %v", task.Target.Platform, task.Target.TokenID, err)
// 		receipt.Status = model.DeliveryStatusFailed
// 		receipt.StatusReason = err.Error()
// 		s.pushToDLQ(ctx, receipt, task)
// 		return
// 	}

// 	receipt.Status = model.DeliveryStatusSuccess
// 	s.pushToDLQ(ctx, receipt, task)
// }
