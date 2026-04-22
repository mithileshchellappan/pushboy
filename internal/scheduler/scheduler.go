package scheduler

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/model"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type Scheduler struct {
	store       storage.Store
	jobPipeline pipeline.Pipeline[model.JobItem]
	stopChan    chan struct{}
	interval    int
	stopOnce    sync.Once
}

const (
	liveActivitySweepBatchSize  = 1000
	liveActivitySweepMaxBatches = 2
)

func New(store storage.Store, jobPipeline pipeline.Pipeline[model.JobItem], interval int) *Scheduler {
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
			ID:      job.ID,
			JobType: model.JobTypePush, //TODO: Support LA type scheduled jobs
			Payload: job.Payload,
			TopicID: job.TopicID,
			UserID:  job.UserID,
		}

		s.jobPipeline.Submit(ctx, jobItem)
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
			if storage.IsLiveActivityUnsupported(err) {
				return nil
			}
			return err
		}
		if count < liveActivitySweepBatchSize {
			return nil
		}
	}
	return nil
}
