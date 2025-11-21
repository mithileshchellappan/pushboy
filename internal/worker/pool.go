package worker

import (
	"context"
	"log"
	"sync"

	"github.com/mithileshchellappan/pushboy/internal/service"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type Pool struct {
	service    *service.PushboyService
	jobChan    chan *storage.PublishJob
	numWorkers int
	wg         sync.WaitGroup
}

func NewPool(svc *service.PushboyService, numWorkers int, queueSize int) *Pool {
	return &Pool{
		service:    svc,
		jobChan:    make(chan *storage.PublishJob, queueSize),
		numWorkers: numWorkers,
		wg:         sync.WaitGroup{},
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
	for job := range p.jobChan {
		log.Printf("Worker %d: Processing job %s", id, job.ID)

		ctx := context.Background()

		if err := p.service.ProcessJobs(ctx, job); err != nil {
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
