package workers

import (
	"context"
	"errors"
	"testing"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
)

func TestPushOutcomeWorkerProcessOutcomeAppliesReceipts(t *testing.T) {
	storeErr := errors.New("push write failed")

	tests := []struct {
		name    string
		err     error
		wantErr bool
	}{
		{name: "success"},
		{name: "error propagates", err: storeErr, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakePushOutcomeWriter{err: tt.err}
			worker := NewPushOutcomeWorker(store, nil, 10, 10)
			deliveries := []pipeline.Delivery[model.SendOutcome]{
				fakeOutcomeDelivery[model.SendOutcome]{outcome: sendOutcome("push-job")},
			}

			err := worker.processOutcome(context.Background(), deliveries)

			if tt.wantErr && !errors.Is(err, storeErr) {
				t.Fatalf("processOutcome error = %v, want %v", err, storeErr)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("processOutcome error = %v, want nil", err)
			}
			if len(store.receipts) != 1 {
				t.Fatalf("push receipts len = %d, want 1", len(store.receipts))
			}
			if got := store.receipts[0].JobID; got != "push-job" {
				t.Fatalf("push receipt JobID = %q, want push-job", got)
			}
		})
	}
}

func TestLAOutcomeWorkerProcessOutcomeAppliesOutcomes(t *testing.T) {
	storeErr := errors.New("la write failed")

	tests := []struct {
		name    string
		err     error
		wantErr bool
	}{
		{name: "success"},
		{name: "error propagates", err: storeErr, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeLAOutcomeWriter{err: tt.err}
			worker := NewLAOutcomeWorker(store, nil, 10, 10)
			deliveries := []pipeline.Delivery[model.LASendOutcome]{
				fakeOutcomeDelivery[model.LASendOutcome]{outcome: laOutcome("la-dispatch")},
			}

			err := worker.processOutcome(context.Background(), deliveries)

			if tt.wantErr && !errors.Is(err, storeErr) {
				t.Fatalf("processOutcome error = %v, want %v", err, storeErr)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("processOutcome error = %v, want nil", err)
			}
			if len(store.outcomes) != 1 {
				t.Fatalf("LA outcomes len = %d, want 1", len(store.outcomes))
			}
			if got := store.outcomes[0].Receipt.JobID; got != "la-dispatch" {
				t.Fatalf("LA outcome JobID = %q, want la-dispatch", got)
			}
		})
	}
}

func TestOutcomeWorkerConstructorsClampInvalidSettings(t *testing.T) {
	pushWorker := NewPushOutcomeWorker(&fakePushOutcomeWriter{}, nil, -1, 0)
	if pushWorker.queueSize != 1 || pushWorker.queueFlushTime != 1 {
		t.Fatalf("push worker settings = %d/%d, want 1/1", pushWorker.queueSize, pushWorker.queueFlushTime)
	}

	laWorker := NewLAOutcomeWorker(&fakeLAOutcomeWriter{}, nil, 0, -1)
	if laWorker.queueSize != 1 || laWorker.queueFlushTime != 1 {
		t.Fatalf("LA worker settings = %d/%d, want 1/1", laWorker.queueSize, laWorker.queueFlushTime)
	}
}

type fakePushOutcomeWriter struct {
	err      error
	receipts []model.DeliveryReceipt
}

func (f *fakePushOutcomeWriter) ApplyPushOutcomeBatch(ctx context.Context, receipts []model.DeliveryReceipt) error {
	f.receipts = append(f.receipts, receipts...)
	return f.err
}

type fakeLAOutcomeWriter struct {
	err      error
	outcomes []model.LASendOutcome
}

func (f *fakeLAOutcomeWriter) ApplyLAOutcomeBatch(ctx context.Context, outcomes []model.LASendOutcome) error {
	f.outcomes = append(f.outcomes, outcomes...)
	return f.err
}

type fakeOutcomeDelivery[T any] struct {
	outcome T
}

func (f fakeOutcomeDelivery[T]) Get() T {
	return f.outcome
}

func (f fakeOutcomeDelivery[T]) Retry(ctx context.Context, maxRetry int) error {
	return nil
}

func sendOutcome(jobID string) model.SendOutcome {
	return model.SendOutcome{
		Task: model.SendTask{
			Job: &model.JobItem{ID: jobID, JobType: model.JobTypePush},
		},
		Receipt: model.DeliveryReceipt{
			JobID:   jobID,
			TokenID: jobID + "-token",
			Status:  model.DeliveryStatusFailed,
		},
	}
}

func laOutcome(dispatchID string) model.LASendOutcome {
	return model.LASendOutcome{
		Task: model.LASendTask{
			LAJob: &model.LAJobItem{DispatchID: dispatchID, Action: model.LiveActivityActionUpdate},
		},
		Receipt: model.DeliveryReceipt{
			JobID:   dispatchID,
			TokenID: dispatchID + "-token",
			Status:  model.DeliveryStatusFailed,
		},
	}
}
