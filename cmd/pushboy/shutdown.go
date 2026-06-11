package main

import (
	"context"
	"log"
	"sync"
)

type pipelineCloser interface {
	Close(ctx context.Context) error
}

// drainStage is one hop of a worker chain: a pipeline feeding a pool of
// workers. Closing the pipeline tells the workers to finish; waiting on the
// WaitGroup confirms they have.
type drainStage struct {
	name    string
	pipe    pipelineCloser
	workers *sync.WaitGroup
}

// drainChain shuts a worker chain down upstream-first: each stage's pipeline
// is closed only after every producer feeding it has exited, so no worker
// ever submits into a closed pipeline.
func drainChain(ctx context.Context, stages []drainStage) {
	for _, stage := range stages {
		if err := stage.pipe.Close(ctx); err != nil {
			log.Printf("%s shutdown error: %v", stage.name, err)
		}
		stage.workers.Wait()
	}
}
