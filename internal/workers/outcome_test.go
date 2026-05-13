package workers

import (
	"context"
	"errors"
	"testing"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
)

func TestProcessOutcomeRetriesOnlyFailedStorageSide(t *testing.T) {
	pushErr := errors.New("push write failed")
	laErr := errors.New("la write failed")

	tests := []struct {
		name            string
		pushErr         error
		laErr           error
		wantRetryTypes  []model.JobType
		wantErrContains []error
	}{
		{name: "both succeed"},
		{name: "push fails", pushErr: pushErr, wantRetryTypes: []model.JobType{model.JobTypePush}, wantErrContains: []error{pushErr}},
		{name: "la fails", laErr: laErr, wantRetryTypes: []model.JobType{model.JobTypeLA}, wantErrContains: []error{laErr}},
		{name: "both fail", pushErr: pushErr, laErr: laErr, wantRetryTypes: []model.JobType{model.JobTypePush, model.JobTypeLA}, wantErrContains: []error{pushErr, laErr}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeOutcomeWriter{pushErr: tt.pushErr, laErr: tt.laErr}
			worker := OutcomeWorker{store: store}
			deliveries := []pipeline.Delivery[model.SendOutcome]{
				fakeOutcomeDelivery{outcome: sendOutcome("push-job", model.JobTypePush)},
				fakeOutcomeDelivery{outcome: sendOutcome("la-dispatch", model.JobTypeLA)},
			}

			retryDeliveries, err := worker.processOutcome(context.Background(), deliveries)

			for _, wantErr := range tt.wantErrContains {
				if !errors.Is(err, wantErr) {
					t.Fatalf("processOutcome error = %v, want containing %v", err, wantErr)
				}
			}
			if len(tt.wantErrContains) == 0 && err != nil {
				t.Fatalf("processOutcome error = %v, want nil", err)
			}
			if len(retryDeliveries) != len(tt.wantRetryTypes) {
				t.Fatalf("retry count = %d, want %d", len(retryDeliveries), len(tt.wantRetryTypes))
			}
			for i, wantType := range tt.wantRetryTypes {
				if got := retryDeliveries[i].Get().Task.Job.JobType; got != wantType {
					t.Fatalf("retry[%d] job type = %q, want %q", i, got, wantType)
				}
			}
			if len(store.pushReceipts) != 1 {
				t.Fatalf("push receipts len = %d, want 1", len(store.pushReceipts))
			}
			if len(store.laOutcomes) != 1 {
				t.Fatalf("la outcomes len = %d, want 1", len(store.laOutcomes))
			}
		})
	}
}

type fakeOutcomeWriter struct {
	pushErr      error
	laErr        error
	pushReceipts []model.DeliveryReceipt
	laOutcomes   []model.SendOutcome
}

func (f *fakeOutcomeWriter) ApplyPushOutcomeBatch(ctx context.Context, receipts []model.DeliveryReceipt) error {
	f.pushReceipts = append(f.pushReceipts, receipts...)
	return f.pushErr
}

func (f *fakeOutcomeWriter) ApplyLAOutcomeBatch(ctx context.Context, outcomes []model.SendOutcome) error {
	f.laOutcomes = append(f.laOutcomes, outcomes...)
	return f.laErr
}

type fakeOutcomeDelivery struct {
	outcome model.SendOutcome
}

func (f fakeOutcomeDelivery) Get() model.SendOutcome {
	return f.outcome
}

func (f fakeOutcomeDelivery) Retry(ctx context.Context, maxRetry int) error {
	return nil
}

func sendOutcome(jobID string, jobType model.JobType) model.SendOutcome {
	return model.SendOutcome{
		Task: model.SendTask{
			Job: &model.JobItem{ID: jobID, JobType: jobType},
		},
		Receipt: model.DeliveryReceipt{
			JobID:   jobID,
			TokenID: jobID + "-token",
			Status:  model.DeliveryStatusFailed,
		},
	}
}
