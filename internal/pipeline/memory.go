package pipeline

import (
	"context"
	"sync"
)

type MemoryPipeline[T any] struct {
	channel chan T
	closeCh chan struct{}
	closed  sync.Once
}

func NewMemoryPipeline[T any](bufferSize int) *MemoryPipeline[T] {
	if bufferSize < 0 {
		bufferSize = 0
	}
	return &MemoryPipeline[T]{
		channel: make(chan T, bufferSize),
		closeCh: make(chan struct{}),
	}
}

func (p *MemoryPipeline[T]) Submit(ctx context.Context, item T) error {
	select {
	case p.channel <- item:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.closeCh:
		return ErrClosed
	}
}

func (p *MemoryPipeline[T]) Receive(ctx context.Context) (Delivery[T], error) {
	for {
		select {
		case item, ok := <-p.channel:
			if !ok {
				return nil, ctx.Err()
			}
			return memoryDelivery[T]{pipe: p, item: item}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-p.closeCh:
			return nil, ErrClosed
		}
	}
}

func (p *MemoryPipeline[T]) Close(ctx context.Context) error {
	p.closed.Do(func() {
		close(p.closeCh)
	})
	return nil
}

type memoryDelivery[T any] struct {
	pipe *MemoryPipeline[T]
	item T
}

func (md memoryDelivery[T]) Get() T {
	return md.item
}

func (md memoryDelivery[T]) Retry(ctx context.Context) error {
	return md.pipe.Submit(ctx, md.item)
}
