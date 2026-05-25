// Package worker provides a bounded concurrency pool.  A global semaphore
// limits the number of sandbox jobs that can execute simultaneously.  Callers
// that acquire a slot run their job; callers that find the pool full block
// until a slot opens (the HTTP handler sets a context deadline so they don't
// block forever).
//
// This satisfies the spec requirement: "requests queue rather than fail."
// Load-shedding (503) is handled one layer up in the HTTP handler when the
// context is cancelled before a slot opens.
package worker

import (
	"context"
	"sync"
)

// Pool is a bounded concurrency gate.  The zero value is not usable;
// call NewPool.
type Pool struct {
	sem chan struct{}
	wg  sync.WaitGroup
}

// NewPool creates a pool that allows at most n concurrent jobs.
func NewPool(n int) *Pool {
	if n < 1 {
		n = 1
	}
	return &Pool{sem: make(chan struct{}, n)}
}

// Cap returns the configured maximum concurrency.
func (p *Pool) Cap() int { return cap(p.sem) }

// InFlight returns the number of currently executing jobs.
func (p *Pool) InFlight() int { return len(p.sem) }

// Run acquires a concurrency slot, calls f, and releases the slot.
// If ctx is cancelled before a slot opens, it returns ctx.Err() immediately
// without calling f.  The caller must not call Run after Wait.
func (p *Pool) Run(ctx context.Context, f func()) error {
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}

	p.wg.Add(1)
	go func() {
		defer func() {
			<-p.sem
			p.wg.Done()
		}()
		f()
	}()
	return nil
}

// Wait blocks until all in-flight jobs have finished.  It is intended for
// graceful shutdown.
func (p *Pool) Wait() {
	p.wg.Wait()
}
