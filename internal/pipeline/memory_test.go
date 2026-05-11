package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryPipelineSubmitReceive(t *testing.T) {
	p := NewMemoryPipeline[string](1)

	if err := p.Submit(context.Background(), "item"); err != nil {
		t.Fatalf("Submit error = %v", err)
	}
	delivery, err := p.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive error = %v", err)
	}
	if got := delivery.Get(); got != "item" {
		t.Fatalf("delivery.Get() = %q, want item", got)
	}
}

func TestMemoryPipelineNegativeBufferRoundTrip(t *testing.T) {
	p := NewMemoryPipeline[string](-10)

	errCh := make(chan error, 1)
	go func() {
		errCh <- p.Submit(context.Background(), "item")
	}()

	delivery, err := p.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive error = %v", err)
	}
	if got := delivery.Get(); got != "item" {
		t.Fatalf("delivery.Get() = %q, want item", got)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Submit error = %v", err)
	}
}

func TestMemoryPipelineCloseIsIdempotent(t *testing.T) {
	p := NewMemoryPipeline[int](1)

	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("first Close error = %v", err)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("second Close error = %v", err)
	}
}

func TestMemoryPipelineSubmitAfterClose(t *testing.T) {
	p := NewMemoryPipeline[int](1)
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("Close error = %v", err)
	}

	if err := p.Submit(context.Background(), 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("Submit after Close error = %v, want ErrClosed", err)
	}
}

func TestMemoryPipelineBlockedReceiveUnblocksOnClose(t *testing.T) {
	p := NewMemoryPipeline[int](0)
	errCh := make(chan error, 1)

	go func() {
		_, err := p.Receive(context.Background())
		errCh <- err
	}()

	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("Close error = %v", err)
	}

	if err := waitErr(t, errCh); !errors.Is(err, ErrClosed) {
		t.Fatalf("Receive error = %v, want ErrClosed", err)
	}
}

func TestMemoryPipelineBlockedSubmitUnblocksOnClose(t *testing.T) {
	p := NewMemoryPipeline[int](0)
	started := make(chan struct{})
	errCh := make(chan error, 1)

	go func() {
		close(started)
		errCh <- p.Submit(context.Background(), 1)
	}()

	<-started
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("Close error = %v", err)
	}

	if err := waitErr(t, errCh); !errors.Is(err, ErrClosed) {
		t.Fatalf("Submit error = %v, want ErrClosed", err)
	}
}

func TestMemoryPipelineBlockedSubmitUnblocksOnContextCancel(t *testing.T) {
	p := NewMemoryPipeline[int](0)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	errCh := make(chan error, 1)

	go func() {
		close(started)
		errCh <- p.Submit(ctx, 1)
	}()

	<-started
	cancel()

	if err := waitErr(t, errCh); !errors.Is(err, context.Canceled) {
		t.Fatalf("Submit error = %v, want context.Canceled", err)
	}
}

func TestMemoryDeliveryRetryAfterClose(t *testing.T) {
	p := NewMemoryPipeline[string](1)
	if err := p.Submit(context.Background(), "item"); err != nil {
		t.Fatalf("Submit error = %v", err)
	}
	delivery, err := p.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive error = %v", err)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("Close error = %v", err)
	}

	if err := delivery.Retry(context.Background(), 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("Retry after Close error = %v, want ErrClosed", err)
	}
}

func waitErr(t *testing.T, errCh <-chan error) error {
	t.Helper()

	select {
	case err := <-errCh:
		return err
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for pipeline operation")
		return nil
	}
}
