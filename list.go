// Package wait provides a waitlist for pooling reusable resources.
//
// A [List] manages a pool of items with two key properties: waiters are
// served in FIFO order, and item creation is lazy up to a configurable limit.
// This makes it suitable for expensive resources like database connections
// where fairness matters and you want to avoid creating more than necessary.
//
// Unlike sync.Pool, which is designed for reducing allocation overhead of
// temporary objects, List bounds resource creation and guarantees FIFO fairness.
//
// List trades some throughput for predictable latency. If you don't need
// fairness or creation limits, a buffered channel is simpler.
// Checked-out items return to the pool with [List.Put] or permanently leave
// it with [List.Retire].
package wait

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"blake.io/wait/queue"
)

var (
	// ErrMaxWaiters is returned by [List.Take] when MaxWaiters is exceeded.
	ErrMaxWaiters = errors.New("too many waiters")

	// ErrClosed is returned by [List.Take] when the List is closed.
	ErrClosed = errors.New("closed")
)

// List is a waitlist for pooling items of type Item.
//
// Waiters are served in FIFO order. When no waiters are present, ready
// items are stored in a LIFO stack. When the ready queue is empty and
// MaxItems allows, Take spawns a goroutine to create a new item with [List.New].
//
// The zero value is a usable List with no limits.
// It is safe for concurrent use.
type List[Item any] struct {
	// MaxItems is the maximum number of items to create via New.
	// Zero means no limit.
	MaxItems int

	// New creates an item when the ready queue is empty and MaxItems allows.
	// If nil, New returns the zero value of Item.
	New func(context.Context) Item

	// MaxWaiters is the maximum number of goroutines that can wait.
	// Take returns ErrMaxWaiters when this limit is reached.
	// Zero means no limit.
	MaxWaiters int

	// readyMu guards the ready list and loads counter.
	readyMu sync.Mutex
	ready   queue.Lifo[Item] // LIFO stack of ready items
	loads   int              // number of live items tracked by the list

	// waitersMu guards waiters and testHookWaiterCanceled.
	waitersMu sync.Mutex
	waiters   queue.Fifo[*waiter[Item]] // FIFO queue of waiters

	chanPool sync.Pool

	closed atomic.Bool

	testHookWaiterCanceled func(ch chan Item)
}

type waiter[T any] struct {
	ctx context.Context
	ch  chan T
}

// Close closes the List. Waiting goroutines are unblocked and will
// receive [ErrClosed]. Ready items can still be drained via Take or
// TryTake. Future Put calls return false. Close is idempotent.
func (p *List[T]) Close() {
	if p.closed.Swap(true) {
		// Already closed
		return
	}

	p.waitersMu.Lock()
	defer p.waitersMu.Unlock()

	// Wake all waiters
	for {
		waiter, ok := p.waiters.Shift()
		if !ok {
			break
		}
		close(waiter.ch)
	}
}

// Take returns an item from the List, blocking until one is available,
// ctx is done, or the List is closed.
//
// If a ready item exists, Take returns it immediately regardless of ctx
// or close state. Otherwise, if MaxItems has not been reached, Take spawns
// a goroutine to call New (or a function returning the zero value if New
// is nil) and waits in FIFO order for a result.
//
// Take returns [ErrMaxWaiters] if the waiter limit is reached.
// Take returns [ErrClosed] when closed with no ready items remaining.
// Take returns the context error if ctx is canceled before receiving an item.
func (p *List[T]) Take(ctx context.Context) (T, error) {
	var zero T

	p.readyMu.Lock()

	// Check for ready item first, even if closed or context done.
	// This allows draining ready items after close, like a channel.
	if v, ok := p.ready.Pop(); ok {
		p.readyMu.Unlock()
		return v, nil
	}

	// No ready items available. Check if we should proceed to wait.
	if p.closed.Load() {
		p.readyMu.Unlock()
		return zero, ErrClosed
	}

	if ctx.Err() != nil {
		p.readyMu.Unlock()
		return zero, context.Cause(ctx)
	}

	p.waitersMu.Lock()

	// Check MaxWaiters limit
	if p.MaxWaiters > 0 && p.waiters.Len() >= p.MaxWaiters {
		p.waitersMu.Unlock()
		p.readyMu.Unlock()
		return zero, ErrMaxWaiters
	}

	// Get in line before we start any loading to ensure FIFO order.
	ch, _ := p.chanPool.Get().(chan T)
	if ch == nil {
		ch = make(chan T, 1)
	}
	waiter := &waiter[T]{
		ctx: ctx,
		ch:  ch,
	}
	p.waiters.Unshift(waiter)

	if ctx.Err() == nil {
		p.startNextLoadLocked()
	}

	p.waitersMu.Unlock()
	p.readyMu.Unlock()

	// Wait for value or context cancellation
	select {
	case v, ok := <-waiter.ch:
		p.chanPool.Put(waiter.ch)
		if !ok {
			return zero, ErrClosed
		}
		return v, nil
	case <-ctx.Done():
		err := context.Cause(ctx)
		v, err := p.handleCancel(waiter, err)
		p.chanPool.Put(waiter.ch)
		return v, err
	}
}

// TryTake returns the next ready item without blocking and ok=true; otherwise,
// it returns a zero value and ok=false.
// Unlike [Take], it never waits and never spawns New goroutines.
func (p *List[T]) TryTake() (_ T, ok bool) {
	p.readyMu.Lock()
	defer p.readyMu.Unlock()
	return p.ready.Pop()
}

// Put adds v to the List. If waiters exist, v is handed to the longest-waiting
// goroutine in FIFO order. Otherwise, v is added to the ready stack in LIFO order.
//
// Put returns false if the List is closed, true otherwise.
// Put does not block.
func (p *List[T]) Put(v T) (accepted bool) {
	if p.closed.Load() {
		return false
	}

	maybeHandoff := func(v T) bool {
		p.waitersMu.Lock()
		waiter, ok := p.waiters.Shift()
		p.waitersMu.Unlock()
		if ok {
			select {
			case waiter.ch <- v:
			default:
				panic("waiter: waiter channel full (this is a bug in waitlist)")
			}
		}
		return ok
	}

	// Attempt a handoff without locking readyMu to fast-path high-load
	// cases.
	if maybeHandoff(v) {
		return true
	}

	p.readyMu.Lock()
	// If closed while acquiring readyMu, bail.
	if p.closed.Load() {
		p.readyMu.Unlock()
		return false
	}

	// We may have accumulated waiters while waiting for readyMu.
	// Handoff to one if so.
	if maybeHandoff(v) {
		p.readyMu.Unlock()
		return true
	}

	p.ready.Push(v)
	p.readyMu.Unlock()
	return true
}

// Retire permanently removes one checked-out item from the live item count.
//
// If the List is open and there is a waiting goroutine, Retire starts exactly
// one replacement load using [List.New] with the oldest waiter's context.
// If there are no live items to retire, Retire is a no-op.
func (p *List[T]) Retire() {
	p.readyMu.Lock()
	defer p.readyMu.Unlock()

	if p.loads == 0 {
		return
	}
	p.loads--

	p.waitersMu.Lock()
	defer p.waitersMu.Unlock()

	if p.closed.Load() {
		return
	}

	p.startNextLoadLocked()
}

// handleCancel removes the given waiter from the waiters list and returns
// errUnlessMissed unless a near-miss occurred and a value is available on the
// waiter channel, in which case it returns that value and nil error.
func (p *List[T]) handleCancel(w *waiter[T], errUnlessMissed error) (T, error) {
	var zero T

	if p.testHookWaiterCanceled != nil {
		p.testHookWaiterCanceled(w.ch)
	}

	p.waitersMu.Lock()
	p.waiters.DeleteFunc(func(queued *waiter[T]) bool {
		return w == queued
	})
	p.waitersMu.Unlock()

	select {
	case v := <-w.ch:
		// Near miss: a value arrived just as we were canceling.
		// Return it instead of the error.
		return v, nil
	default:
	}
	return zero, errUnlessMissed
}

func normalizeNew[T any](new func(context.Context) T) func(context.Context) T {
	if new != nil {
		return new
	}
	return func(context.Context) (zero T) { return }
}

func (p *List[T]) startNextLoadLocked() {
	if p.MaxItems > 0 && p.loads >= p.MaxItems {
		return
	}

	waiter, ok := p.waiters.Front()
	if !ok {
		return
	}
	p.loads++
	newItem := normalizeNew(p.New)
	go func() { p.Put(newItem(waiter.ctx)) }()
}
