package worker

import (
	"context"
	"log"
	"sync"

	"github.com/mithileshchellappan/pushboy/internal/dispatch"
	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type Pool struct {
	store       storage.Store
	dispatchers map[string]dispatch.Dispatcher
	jobChan     chan *storage.PublishJob
	numWorkers  int
	numSenders  int
	wg          sync.WaitGroup
	closed      bool
	mu          sync.RWMutex
}

func NewPool(store storage.Store, dispatchers map[string]dispatch.Dispatcher, numWorkers int, numSenders, queueSize int) *Pool {
	return &Pool{
		store:       store,
		dispatchers: dispatchers,
		jobChan:     make(chan *storage.PublishJob, queueSize),
		numWorkers:  numWorkers,
		numSenders:  numSenders,
		wg:          sync.WaitGroup{},
	}
}

func (p *Pool) Start() {
	for i := 0; i < p.numWorkers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
}

func (p *Pool) worker(id int) {
	defer p.wg.Done()

	pipe := pipeline.NewNotificationPipeline(p.store, p.dispatchers, p.numSenders, 5000)
	log.Printf("Worker %d: Pipeline created with %d sender", id, p.numSenders)

	for job := range p.jobChan {
		log.Printf("Worker %d: Processing job %s", id, job.ID)

		if err := pipe.ProcessJob(context.Background(), job); err != nil {
			log.Printf("Worker %d: Error processing job %s: %v", id, job.ID, err)
		}
	}

	log.Printf("Worker %d: Exiting", id)
}

func (p *Pool) Submit(job *storage.PublishJob) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		log.Printf("Worker pool is closed, rejecting job %s", job.ID)
		return false
	}
	p.jobChan <- job
	return true
}

func (p *Pool) Stop() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	close(p.jobChan)
	p.wg.Wait()
	log.Println("Worker pool stopped")
}
