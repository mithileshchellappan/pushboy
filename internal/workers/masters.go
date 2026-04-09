package workers

import (
	"fmt"

	"github.com/mithileshchellappan/pushboy/internal/pipeline"
	"github.com/mithileshchellappan/pushboy/internal/storage"
)

type MasterWorker struct {
	store       storage.Store
	jobPipeline *pipeline.JobPipeline
	// workPipeline
}

func NewMaster(store storage.Store, jobPipeline *pipeline.JobPipeline) MasterWorker {
	return MasterWorker{
		store:       store,
		jobPipeline: jobPipeline,
	}
}

func (m *MasterWorker) Start() {
	go func() {
		for job := range m.jobPipeline.Jobs() {
			fmt.Println("got job ", job.ID)
		}
	}()
}

func (m *MasterWorker) Stop() {

}
