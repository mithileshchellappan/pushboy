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

	pipe := pipeline.NewNotificationPipeline(p.store, p.dispatchers, p.numSenders, 1000)
	log.Printf("Worker %d: Pipeline created with %d sender", id, p.numSenders)

	for job := range p.jobChan {
		log.Printf("Worker %d: Processing job %s", id, job.ID)

		if err := pipe.ProcessJob(context.Background(), job); err != nil {
			log.Printf("Worker %d: Error processing job %s: %v", id, job.ID, err)
		}
	}

	log.Printf("Worker %d: Exiting", id)
}

func (p *Pool) Submit(job *storage.PublishJob) {
	p.jobChan <- job //adding into channel
}

func (p *Pool) Stop() {
	close(p.jobChan)
	p.wg.Wait()
	log.Println("Worker pool stopped")
}
