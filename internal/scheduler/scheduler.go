package scheduler

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type schedulerStore interface {
	FailTenantJobDispatch(context.Context, uuid.UUID, string) error
	GetScheduledJobs(ctx context.Context) ([]storage.PublishJob, error)
	UpdateJobStatus(ctx context.Context, jobID string, status model.NotificationJobStatus) error
	InvalidateExpiredLAUpdateTokens(ctx context.Context, limit int) (int, error)
}

type Scheduler struct {
	store       schedulerStore
	jobPipeline pipeline.Pipeline[model.JobItem]
	stopChan    chan struct{}
	interval    int
	stopOnce    sync.Once
}

const (
	liveActivitySweepBatchSize  = 1000
	liveActivitySweepMaxBatches = 2
)

func New(store schedulerStore, jobPipeline pipeline.Pipeline[model.JobItem], interval int) *Scheduler {
	return &Scheduler{
		store:       store,
		jobPipeline: jobPipeline,
		interval:    interval,
		stopChan:    make(chan struct{}),
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Duration(s.interval) * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				s.processScheduledJobs(ctx)
				s.sweepLiveActivityState(ctx)
			case <-s.stopChan:
				return
			}
		}
	}()
}

func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopChan)
	})
}

func (s *Scheduler) processScheduledJobs(ctx context.Context) {
	jobs, err := s.store.GetScheduledJobs(ctx)
	if err != nil {
		log.Printf("Error starting scheduled jobs: %v", err)
		return
	}

	for _, job := range jobs {

		jobItem := model.JobItem{
			ID:       job.ID,
			TenantID: job.TenantID,
			JobType:  model.JobTypePush,
			Payload:  job.Payload,
			TopicID:  job.TopicID,
			UserID:   job.UserID,
		}

		if err := s.jobPipeline.Submit(ctx, jobItem); err != nil {
			statusCtx, statusCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer statusCancel()

			var updateErr error
			if job.TenantID != uuid.Nil {
				updateErr = s.store.FailTenantJobDispatch(statusCtx, job.TenantID, job.ID)
			} else {
				updateErr = s.store.UpdateJobStatus(statusCtx, jobItem.ID, model.NotificationJobStatusFailed)
			}
			if updateErr != nil {
				log.Printf("Failed to mark job %s as FAILED after enqueue error: %v", jobItem.ID, updateErr)
			}
		}
	}
}

func (s *Scheduler) sweepLiveActivityState(ctx context.Context) {
	if err := s.runLiveActivitySweep(ctx, s.store.InvalidateExpiredLAUpdateTokens); err != nil {
		log.Printf("Error invalidating expired live activity update tokens: %v", err)
	}
}

func (s *Scheduler) runLiveActivitySweep(ctx context.Context, sweep func(context.Context, int) (int, error)) error {
	for i := 0; i < liveActivitySweepMaxBatches; i++ {
		count, err := sweep(ctx, liveActivitySweepBatchSize)
		if err != nil {
			return err
		}
		if count < liveActivitySweepBatchSize {
			return nil
		}
	}
	return nil
}
