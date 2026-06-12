package workers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
)

type DispatchFunc[T any, O any] func(
	ctx context.Context,
	task T,
) error

func DispatchPushTask(ctx context.Context, task model.SendTask, dispatchers map[model.Platform]dispatch.Dispatcher, dlqPipeline pipeline.Pipeline[model.SendOutcome]) error {
	dispatcher, ok := dispatchers[task.Target.Platform]
	receipt := model.DeliveryReceipt{
		ID:           uuid.New().String(),
		JobID:        task.Job.ID,
		TokenID:      task.Target.TokenID,
		DispatchedAt: time.Now().UTC(),
	}
	if !ok {
		receipt.Status = model.DeliveryStatusFailed
		receipt.StatusReason = fmt.Sprintf("Unknown dispatcher platform: %s", task.Target.Platform)
		outcome := model.SendOutcome{
					Task: task,
					Receipt: receipt,
		}
		pushToDLQ(ctx, outcome, dlqPipeline)
		return errors.New(receipt.StatusReason)
	}

	pushDispatcher, ok := dispatcher.(dispatch.Dispatcher)
	if !ok {
		receipt.Status = model.DeliveryStatusFailed
		receipt.StatusReason = fmt.Sprintf("Unknown dispatcher platform: %s", task.Target.Platform)
		outcome := model.SendOutcome{
					Task: task,
					Receipt: receipt,
		}
		pushToDLQ(ctx, outcome, dlqPipeline)
		return errors.New(receipt.StatusReason)
	}

	err := pushDispatcher.Send(ctx, task.Target.Token, task.Job.Payload)
	if err != nil {
		fmt.Printf("Error sending %s notification, tokenId: %s, error: %v", task.Target.Platform, task.Target.TokenID, err)
		receipt.Status = model.DeliveryStatusFailed
		receipt.StatusReason = err.Error()
		outcome := model.SendOutcome{
					Task: task,
					Receipt: receipt,
		}
		pushToDLQ(ctx, outcome, dlqPipeline)
		return errors.New(receipt.StatusReason)
	}
	receipt.Status = model.DeliveryStatusSuccess
	outcome := model.SendOutcome{
				Task: task,
				Receipt: receipt,
	}
	pushToDLQ(ctx, outcome, dlqPipeline)
	return nil
}

func DispatchLATask(ctx context.Context, task model.LASendTask, dispatchers map[model.Platform]dispatch.Dispatcher, dlqPipeline pipeline.Pipeline[model.LASendOutcome]) {
	dispatcher, ok := dispatchers[task.Target.Platform]
	receipt := model.DeliveryReceipt{
		ID:           uuid.New().String(),
		JobID:        task.LAJob.JobID,
		TokenID:      task.Target.TokenID,
		DispatchedAt: time.Now().UTC(),
	}
	if !ok {
		receipt.Status = model.DeliveryStatusFailed
		receipt.StatusReason = fmt.Sprintf("Unknown dispatcher platform: %s", task.Target.Platform)
		outcome := model.LASendOutcome{
			Task: task,
			Receipt: receipt,
		}
		pushToDLQ[model.LASendOutcome](ctx, outcome, dlqPipeline)
		return
	}

	laDispatcher, ok := dispatcher.(dispatch.LiveActivityDispatcher)
	if !ok {
		receipt.Status = model.DeliveryStatusFailed
		receipt.StatusReason = fmt.Sprintf("No live activity dispatcher configured for platform: %s", task.Target.Platform)
		outcome := model.LASendOutcome{
			Task: task,
			Receipt: receipt,
		}
		pushToDLQ[model.LASendOutcome](ctx, outcome, dlqPipeline)
		return
	}

	err := laDispatcher.SendLiveActivity(ctx, task.Target.Token, &model.LiveActivityRequest{
		Action:       task.LAJob.Action,
		ActivityID:   task.LAJob.ActivityID,
		ActivityType: task.LAJob.Activity,
		Payload:      task.LAJob.Payload,
		Options:      task.LAJob.Options,
	})
	if err != nil {
		fmt.Printf("Error sending %s live activity, tokenId: %s, error: %v", task.Target.Platform, task.Target.TokenID, err)
		receipt.Status = model.DeliveryStatusFailed
		receipt.StatusReason = err.Error()
		outcome := model.LASendOutcome{
			Task: task,
			Receipt: receipt,
		}
		pushToDLQ[model.LASendOutcome](ctx, outcome, dlqPipeline)
		return
	}

	receipt.Status = model.DeliveryStatusSuccess
	outcome := model.LASendOutcome{
		Task: task,
		Receipt: receipt,
	}
	pushToDLQ[model.LASendOutcome](ctx, outcome, dlqPipeline)
}


func pushToDLQ[O any](ctx context.Context, outcome O, dlqPipeline pipeline.Pipeline[O]) {
	err := dlqPipeline.Submit(ctx, outcome)

	if errors.Is(err, context.Canceled) {
		return
	}

	if err != nil {
		err2 := dlqPipeline.Submit(ctx, outcome) //try resubmitting once
		if err2 != nil {
			fmt.Printf("Error submitting failed job to dlq try 1: %v try 2: %v", err.Error(), err2.Error())
		}
	}
	return
}
