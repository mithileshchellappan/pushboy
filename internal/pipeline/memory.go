package pipeline

import (
	"context"
	"fmt"
	"sync"
)

type MemoryPipeline[T any] struct {
	channel chan T
	mu      sync.RWMutex
	closed  bool
}

func NewMemoryPipeline[T any](bufferSize int) *MemoryPipeline[T] {
	if bufferSize < 0 {
		bufferSize = 0
	}
	return &MemoryPipeline[T]{
		channel: make(chan T, bufferSize),
	}
}

func (p *MemoryPipeline[T]) Submit(ctx context.Context, item T) error {
	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return ErrClosed
	}
	defer p.mu.RUnlock()
	select {
	case p.channel <- item:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *MemoryPipeline[T]) Receive(ctx context.Context) (Delivery[T], error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ErrClosed
		case item, ok := <-p.channel:
			if !ok {
				return nil, ErrClosed
			}
			return memoryDelivery[T]{pipe: p, item: item, retryCount: 0}, nil

		}
	}
}

func (p *MemoryPipeline[T]) Close(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}

	p.closed = true
	close(p.channel)

	return nil
}

type memoryDelivery[T any] struct {
	pipe       *MemoryPipeline[T]
	item       T
	retryCount int
}

func (md memoryDelivery[T]) Get() T {
	return md.item
}

func (md memoryDelivery[T]) Retry(ctx context.Context, maxRetry int) error {

	if md.retryCount > maxRetry {
		return fmt.Errorf("Max retry rechead")
	}
	md.retryCount++
	return md.pipe.Submit(ctx, md.item)
}
