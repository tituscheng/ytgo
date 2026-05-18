// Package pipeline provides the concurrent pipeline architecture for ytgo.
package pipeline

import (
	"context"

	"golang.org/x/sync/errgroup"
)

// WorkerPool is a bounded pool of goroutines backed by errgroup.Group.
// It provides cancellation propagation and first-error termination.
type WorkerPool struct {
	limit int
	sem   chan struct{}
	eg    *errgroup.Group
}

// NewWorkerPool creates a WorkerPool that runs at most 'limit' goroutines
// concurrently. A limit <= 0 means unlimited concurrency.
func NewWorkerPool(limit int) *WorkerPool {
	return &WorkerPool{
		limit: limit,
		sem:   make(chan struct{}, limit),
	}
}

// Start begins the errgroup with the given parent context.
// Call this before Submit.
func (wp *WorkerPool) Start(ctx context.Context) {
	wp.eg, _ = errgroup.WithContext(ctx)
}

// Submit enqueues fn to be executed by the pool. If the pool is at capacity,
// Submit blocks until a worker becomes available. Returns immediately if the
// context is cancelled.
func (wp *WorkerPool) Submit(ctx context.Context, fn func() error) error {
	if wp.limit <= 0 {
		wp.eg.Go(fn)
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case wp.sem <- struct{}{}:
		wp.eg.Go(func() error {
			defer func() { <-wp.sem }()
			return fn()
		})
		return nil
	}
}

// Wait blocks until all submitted work completes. It returns the first
// non-nil error encountered by any worker, or nil if all succeed.
func (wp *WorkerPool) Wait() error {
	if wp.eg == nil {
		return nil
	}
	return wp.eg.Wait()
}
