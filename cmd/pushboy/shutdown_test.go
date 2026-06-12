package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mithileshchellappan/pushboy/internal/pipeline"
)

// eventLog records the order of shutdown events across goroutines.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// recordingPipe wraps a pipeline so the test can see when each stage closes.
type recordingPipe struct {
	name  string
	inner pipelineCloser
	log   *eventLog
}

func (p recordingPipe) Close(ctx context.Context) error {
	p.log.add("close:" + p.name)
	return p.inner.Close(ctx)
}

// startWorker runs a single fake worker that blocks on Receive until the
// pipeline closes, then records its exit — mimicking how masters, senders,
// and the outcome worker react to ErrClosed.
func startWorker(p *pipeline.MemoryPipeline[int], name string, log *eventLog, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		for {
			_, err := p.Receive(context.Background())
			if errors.Is(err, pipeline.ErrClosed) {
				log.add("workers-exited:" + name)
				wg.Done()
				return
			}
		}
	}()
}

func TestDrainChainClosesStagesUpstreamFirst(t *testing.T) {
	log := &eventLog{}

	jobPipe := pipeline.NewMemoryPipeline[int](1)
	taskPipe := pipeline.NewMemoryPipeline[int](1)
	dlqPipe := pipeline.NewMemoryPipeline[int](1)

	var masterWg, senderWg, outcomeWg sync.WaitGroup
	startWorker(jobPipe, "job", log, &masterWg)
	startWorker(taskPipe, "task", log, &senderWg)
	startWorker(dlqPipe, "dlq", log, &outcomeWg)

	done := make(chan struct{})
	go func() {
		drainChain(context.Background(), []drainStage{
			{name: "job", pipe: recordingPipe{"job", jobPipe, log}, workers: &masterWg},
			{name: "task", pipe: recordingPipe{"task", taskPipe, log}, workers: &senderWg},
			{name: "dlq", pipe: recordingPipe{"dlq", dlqPipe, log}, workers: &outcomeWg},
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("drainChain did not finish; a stage is stuck waiting on its workers")
	}

	want := []string{
		"close:job", "workers-exited:job",
		"close:task", "workers-exited:task",
		"close:dlq", "workers-exited:dlq",
	}
	got := log.snapshot()
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] = %q, want %q (full order: %v)\n"+
				"a downstream pipeline closed before its upstream workers exited",
				i, got[i], want[i], got)
		}
	}
}

func TestDrainChainToleratesAlreadyClosedPipeline(t *testing.T) {
	p := pipeline.NewMemoryPipeline[int](1)
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("first close: %v", err)
	}

	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		drainChain(context.Background(), []drainStage{
			{name: "job", pipe: p, workers: &wg},
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("drainChain hung on an already-closed pipeline")
	}
}

// TestParallelDrainJoinWaitsForBothChains pins the shutdown join pattern used
// in main: two chains drain concurrently, and the shared cleanup (store close,
// gracefulDone) must run only after BOTH finish — not whichever is first.
func TestParallelDrainJoinWaitsForBothChains(t *testing.T) {
	fastPipe := pipeline.NewMemoryPipeline[int](1)
	slowPipe := pipeline.NewMemoryPipeline[int](1)

	var fastWg, slowWg sync.WaitGroup
	log := &eventLog{}
	startWorker(fastPipe, "fast", log, &fastWg)

	// the slow chain's worker takes 100ms to exit after close
	slowWg.Add(1)
	go func() {
		_, err := slowPipe.Receive(context.Background())
		if errors.Is(err, pipeline.ErrClosed) {
			time.Sleep(100 * time.Millisecond)
			log.add("workers-exited:slow")
			slowWg.Done()
		}
	}()

	var drainWg sync.WaitGroup
	drainWg.Add(2)
	go func() {
		defer drainWg.Done()
		drainChain(context.Background(), []drainStage{{name: "fast", pipe: fastPipe, workers: &fastWg}})
	}()
	go func() {
		defer drainWg.Done()
		drainChain(context.Background(), []drainStage{{name: "slow", pipe: slowPipe, workers: &slowWg}})
	}()

	gracefulDone := make(chan struct{})
	go func() {
		drainWg.Wait()
		log.add("join")
		close(gracefulDone)
	}()

	select {
	case <-gracefulDone:
	case <-time.After(5 * time.Second):
		t.Fatal("join never fired")
	}

	got := log.snapshot()
	if got[len(got)-1] != "join" {
		t.Fatalf("join fired before both chains drained: %v", got)
	}
	seen := false
	for _, e := range got {
		if e == "workers-exited:slow" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("slow chain never drained before join: %v", got)
	}
}
