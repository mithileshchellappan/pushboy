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
	store                   storage.Store
	notificationDispatchers map[string]dispatch.Dispatcher
	laDispatchers           map[string]dispatch.LADispatcher
	jobChan                 chan *WorkItem
	numWorkers              int
	numSenders              int
	batchSize               int
	wg                      sync.WaitGroup
	closed                  bool
	mu                      sync.RWMutex
}

func NewPool(store storage.Store, notificationDispatchers map[string]dispatch.Dispatcher, laDispatchers map[string]dispatch.LADispatcher, numWorkers, numSenders, queueSize, batchSize int) *Pool {
	return &Pool{
		store:                   store,
		notificationDispatchers: notificationDispatchers,
		laDispatchers:           laDispatchers,
		jobChan:                 make(chan *WorkItem, queueSize),
		numWorkers:              numWorkers,
		numSenders:              numSenders,
		batchSize:               batchSize,
		wg:                      sync.WaitGroup{},
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

	notificationPipe := pipeline.NewNotificationPipeline(p.store, p.notificationDispatchers, p.numSenders, p.batchSize)
	laPipe := pipeline.NewLAPipeline(p.store, p.laDispatchers, p.numSenders)
	log.Printf("Worker %d: Pipelines created with %d senders, batch size %d", id, p.numSenders, p.batchSize)

	for job := range p.jobChan {
		log.Printf("Worker %d: Processing job %s", id, job.ID)
		if job.Kind == WorkKindNotification && job.Notification != nil {
			if err := notificationPipe.ProcessJob(context.Background(), &job.Notification.NotificationSnapshot); err != nil {
				log.Printf("Worker %d: Error processing notification job %s: %v", id, job.ID, err)
			}
			continue
		}
		if job.Kind == WorkKindLA && job.LA != nil {
			if err := laPipe.ProcessJob(context.Background(), &job.LA.LASnapshot, job.LA.ClaimedVersion, job.LA.ClaimToken); err != nil {
				log.Printf("Worker %d: Error processing LA job %s: %v", id, job.ID, err)
			}
			continue
		}
		log.Printf("Worker %d: Ignoring malformed work item %s", id, job.ID)
	}

	log.Printf("Worker %d: Exiting", id)
}

func (p *Pool) Submit(job *WorkItem) bool {
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
