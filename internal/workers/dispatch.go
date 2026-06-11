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

func DispatchPushTask(ctx context.Context, task model.SendTask, dispatchers  map[model.Platform]dispatch.Dispatcher, dlqPipeline pipeline.Pipeline[model.SendOutcome] ) error {
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
		pushToDLQ(ctx, receipt, task, dlqPipeline)
		return fmt.Errorf(receipt.StatusReason)
	}

	pushDispatcher, ok := dispatcher.(dispatch.Dispatcher)
	if !ok {
		receipt.Status = model.DeliveryStatusFailed
		receipt.StatusReason = fmt.Sprintf("Unknown dispatcher platform: %s", task.Target.Platform)
		pushToDLQ(ctx, receipt, task, dlqPipeline)
		return fmt.Errorf(receipt.StatusReason)
	}

	err := pushDispatcher.Send(ctx, task.Target.Token, task.Job.Payload)
	if err != nil {
		fmt.Printf("Error sending %s notification, tokenId: %s, error: %v", task.Target.Platform, task.Target.TokenID, err)
		receipt.Status = model.DeliveryStatusFailed
		receipt.StatusReason = err.Error()
		pushToDLQ(ctx, receipt, task, dlqPipeline)
		return fmt.Errorf(receipt.StatusReason)
	}
	receipt.Status = model.DeliveryStatusSuccess
	pushToDLQ(ctx, receipt, task, dlqPipeline)
	return nil
}

func  pushToDLQ(ctx context.Context, receipt model.DeliveryReceipt, task model.SendTask, dlqPipeline pipeline.Pipeline[model.SendOutcome]) {
	outcome := model.SendOutcome{
		Receipt: receipt,
		Task:    task,
	}

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
