package scheduler

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

func TestProcessScheduledJobsEnqueuesReturnedJobs(t *testing.T) {
	payload := &model.NotificationPayload{Title: "hello"}
	store := &fakeSchedulerStore{
		jobs: []storage.PublishJob{
			{ID: "job-1", TopicID: "topic-1", Payload: payload},
			{ID: "job-2", UserID: "user-1", Payload: payload},
		},
	}
	jobPipeline := &fakeJobPipeline{}
	s := New(store, jobPipeline, 10)

	s.processScheduledJobs(context.Background())

	want := []model.JobItem{
		{ID: "job-1", JobType: model.JobTypePush, TopicID: "topic-1", Payload: payload},
		{ID: "job-2", JobType: model.JobTypePush, UserID: "user-1", Payload: payload},
	}
	if !reflect.DeepEqual(jobPipeline.submitted, want) {
		t.Fatalf("submitted jobs = %#v, want %#v", jobPipeline.submitted, want)
	}
	if len(store.statusUpdates) != 0 {
		t.Fatalf("status updates = %#v, want none", store.statusUpdates)
	}
}

func TestProcessScheduledJobsFetchErrorDoesNotEnqueue(t *testing.T) {
	store := &fakeSchedulerStore{getScheduledErr: errors.New("db down")}
	jobPipeline := &fakeJobPipeline{}
	s := New(store, jobPipeline, 10)

	s.processScheduledJobs(context.Background())

	if len(jobPipeline.submitted) != 0 {
		t.Fatalf("submitted jobs = %#v, want none", jobPipeline.submitted)
	}
	if len(store.statusUpdates) != 0 {
		t.Fatalf("status updates = %#v, want none", store.statusUpdates)
	}
}

func TestProcessScheduledJobsMarksFailedWhenSubmitFails(t *testing.T) {
	store := &fakeSchedulerStore{
		jobs: []storage.PublishJob{{ID: "job-1"}},
	}
	jobPipeline := &fakeJobPipeline{submitErr: errors.New("queue closed")}
	s := New(store, jobPipeline, 10)

	s.processScheduledJobs(context.Background())

	if len(store.statusUpdates) != 1 {
		t.Fatalf("status update count = %d, want 1", len(store.statusUpdates))
	}
	if store.statusUpdates[0].jobID != "job-1" || store.statusUpdates[0].status != model.NotificationJobStatusFailed {
		t.Fatalf("status update = %#v, want job-1 FAILED", store.statusUpdates[0])
	}
}

func TestRunLiveActivitySweep(t *testing.T) {
	t.Run("stops after short batch", func(t *testing.T) {
		s := New(&fakeSchedulerStore{}, &fakeJobPipeline{}, 10)
		calls := 0

		err := s.runLiveActivitySweep(context.Background(), func(ctx context.Context, limit int) (int, error) {
			calls++
			if limit != liveActivitySweepBatchSize {
				t.Fatalf("limit = %d, want %d", limit, liveActivitySweepBatchSize)
			}
			return 10, nil
		})

		if err != nil {
			t.Fatalf("runLiveActivitySweep error = %v", err)
		}
		if calls != 1 {
			t.Fatalf("calls = %d, want 1", calls)
		}
	})

	t.Run("caps full batches", func(t *testing.T) {
		s := New(&fakeSchedulerStore{}, &fakeJobPipeline{}, 10)
		calls := 0

		err := s.runLiveActivitySweep(context.Background(), func(ctx context.Context, limit int) (int, error) {
			calls++
			return liveActivitySweepBatchSize, nil
		})

		if err != nil {
			t.Fatalf("runLiveActivitySweep error = %v", err)
		}
		if calls != liveActivitySweepMaxBatches {
			t.Fatalf("calls = %d, want %d", calls, liveActivitySweepMaxBatches)
		}
	})

	t.Run("returns sweep error", func(t *testing.T) {
		s := New(&fakeSchedulerStore{}, &fakeJobPipeline{}, 10)
		wantErr := errors.New("sweep failed")

		err := s.runLiveActivitySweep(context.Background(), func(ctx context.Context, limit int) (int, error) {
			return 0, wantErr
		})

		if !errors.Is(err, wantErr) {
			t.Fatalf("runLiveActivitySweep error = %v, want %v", err, wantErr)
		}
	})
}

type fakeSchedulerStore struct {
	jobs            []storage.PublishJob
	getScheduledErr error
	statusUpdates   []statusUpdate
	updateStatusErr error
}

type statusUpdate struct {
	jobID  string
	status model.NotificationJobStatus
}

func (f *fakeSchedulerStore) GetScheduledJobs(ctx context.Context) ([]storage.PublishJob, error) {
	return f.jobs, f.getScheduledErr
}

func (f *fakeSchedulerStore) UpdateJobStatus(ctx context.Context, jobID string, status model.NotificationJobStatus) error {
	f.statusUpdates = append(f.statusUpdates, statusUpdate{jobID: jobID, status: status})
	return f.updateStatusErr
}

func (f *fakeSchedulerStore) InvalidateExpiredLAUpdateTokens(ctx context.Context, limit int) (int, error) {
	return 0, nil
}

type fakeJobPipeline struct {
	submitted []model.JobItem
	submitErr error
}

func (f *fakeJobPipeline) Submit(ctx context.Context, item model.JobItem) error {
	if f.submitErr != nil {
		return f.submitErr
	}
	f.submitted = append(f.submitted, item)
	return nil
}

func (f *fakeJobPipeline) Receive(ctx context.Context) (pipeline.Delivery[model.JobItem], error) {
	return nil, pipeline.ErrClosed
}

func (f *fakeJobPipeline) Close(ctx context.Context) error {
	return nil
}
