package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/storage"
	"github.com/mithileshchellappan/pushboy/internal/worker"
)

type Scheduler struct {
	store      storage.Store
	workerPool *worker.Pool
	stopChan   chan struct{}
	interval   int
}

func New(store storage.Store, workerPool *worker.Pool, interval int) *Scheduler {
	return &Scheduler{
		store:      store,
		workerPool: workerPool,
		interval:   interval,
		stopChan:   make(chan struct{}),
	}
}

func (s *Scheduler) Start() {
	go func() {
		ticker := time.NewTicker(time.Duration(s.interval) * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				s.processScheduledJobs()
			case <-s.stopChan:
				return
			}
		}
	}()
}

func (s *Scheduler) Stop() {
	s.stopChan <- struct{}{}
}

func (s *Scheduler) processScheduledJobs() {
	jobs, err := s.store.GetScheduledJobs(context.Background())
	if err != nil {
		log.Printf("Error starting scheduled jobs: %v", err)
		return
	}

	for _, job := range jobs {
		s.workerPool.Submit(&job)
	}

	return
}
