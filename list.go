// Package wait provides FIFO waitlists for reusable items and capacity.
//
// A [List] pools items added with [List.Put] and can create more lazily up to a
// limit. It gives queued callers items in arrival order and stores unused ones
// in a LIFO stack. The caller returns checked-out items with [List.Put] or
// removes them with [List.Retire].
//
// A [Gate] stores no items. It admits demands in strict arrival order against
// capacity tracked by the caller. Its [Gate.Fill] and [Gate.Refill] callbacks
// update that accounting.
//
// Use a buffered channel when FIFO order and lazy creation limits are not
// needed. Use [sync.Pool] for temporary allocation reuse, not for a bounded
// resource pool.
package wait

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"blake.io/wait/queue"
)

var (
	// ErrMaxWaiters is returned when [List.Take] or [List.Reserve] would
	// exceed MaxWaiters.
	ErrMaxWaiters = errors.New("too many waiters")

	// ErrClosed is returned by [List.Take], [List.Reserve],
	// [Future.Wait], and [Gate.Wait] when the List or Gate is closed.
	ErrClosed = errors.New("closed")
)

// List pools reusable items of type Item.
//
// It pools items added with [List.Put] and can create more lazily up to
// MaxItems. Queued callers receive items in FIFO order; unused items wait in a
// LIFO stack. The caller returns checked-out items with [List.Put] or removes
// them with [List.Retire].
//
// The zero value has no limits and creates zero-valued items if New is nil.
// List is safe for concurrent use.
type List[Item any] struct {
	// MaxItems is the maximum number of items to create via New.
	// Zero means no limit.
	MaxItems int

	// New creates an item when the ready queue is empty and MaxItems allows.
	// If nil, New returns the zero value of Item.
	New func() Item

	// MaxWaiters is the maximum number of pending Take calls and reservations.
	// Take and Reserve return ErrMaxWaiters when this limit is reached.
	// Zero means no limit.
	MaxWaiters int

	// readyMu guards the ready list and loads counter.
	readyMu sync.Mutex
	ready   queue.Lifo[Item] // LIFO stack of ready items
	loads   int              // number of live items tracked by the list

	// waitersMu guards waiters, their reservation state, and testHookWaiterCanceled.
	waitersMu sync.Mutex
	waiters   queue.Fifo[*waiter[Item]] // FIFO queue of waiters

	chanPool sync.Pool

	closed atomic.Bool

	testHookWaiterCanceled func(ch chan Item)
}

type waiter[T any] struct {
	ch      chan T
	future  *Future[T]
	ctx     context.Context // non-nil for a reservation
	stop    func() bool
	done    bool // guarded by waitersMu
	loading bool
}

// Close wakes pending callers with [ErrClosed]. Callers can still drain ready
// items with [List.Take], [List.Reserve], or [List.TryTake]. Later [List.Put]
// calls return false. Close is idempotent.
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
		if waiter.future != nil {
			var zero T
			if waiter.ctx.Err() != nil {
				p.finishFutureLocked(waiter, zero, context.Cause(waiter.ctx))
			} else {
				p.finishFutureLocked(waiter, zero, ErrClosed)
			}
		} else {
			close(waiter.ch)
		}
	}
}

// Reserve queues a request for an item and returns a [Future] for its result.
// It returns [ErrClosed], the context cause, or [ErrMaxWaiters] if it cannot
// queue the request. A ready item is reserved even if the List is closed or
// ctx is canceled.
//
// Cancellation removes a pending reservation and makes [Future.Wait] return
// the context cause. If an item was assigned first, the future holds a
// checkout; call Wait even after cancellation and return or retire any item
// it returns.
func (p *List[T]) Reserve(ctx context.Context) (*Future[T], error) {
	p.readyMu.Lock()
	if v, ok := p.ready.Pop(); ok {
		p.readyMu.Unlock()
		f := newFuture[T]()
		f.resolve(v, nil)
		return f, nil
	}
	if p.closed.Load() {
		p.readyMu.Unlock()
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		p.readyMu.Unlock()
		return nil, context.Cause(ctx)
	}

	p.waitersMu.Lock()
	// Close may have started while we acquired waitersMu.
	if p.closed.Load() {
		p.waitersMu.Unlock()
		p.readyMu.Unlock()
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		p.waitersMu.Unlock()
		p.readyMu.Unlock()
		return nil, context.Cause(ctx)
	}
	if p.MaxWaiters > 0 && p.waiters.Len() >= p.MaxWaiters {
		p.removeCanceledFuturesLocked()
		if p.waiters.Len() >= p.MaxWaiters {
			p.waitersMu.Unlock()
			p.readyMu.Unlock()
			return nil, ErrMaxWaiters
		}
	}

	f := newFuture[T]()
	w := &waiter[T]{future: f, ctx: ctx}
	p.waiters.Unshift(w)
	w.stop = context.AfterFunc(ctx, func() { p.cancelFuture(w) })
	if ctx.Err() == nil {
		p.startNextLoadLocked()
	}
	p.waitersMu.Unlock()
	p.readyMu.Unlock()
	return f, nil
}

// Take returns an item, waiting until one is available, ctx is done, or the
// List is closed.
//
// A ready item takes precedence over cancellation and close. Otherwise, Take
// returns [ErrClosed] when closed, [ErrMaxWaiters] when the waiter limit is
// reached, or the context cause when canceled before an item is assigned.
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
	if p.closed.Load() {
		p.waitersMu.Unlock()
		p.readyMu.Unlock()
		return zero, ErrClosed
	}
	if p.MaxWaiters > 0 && p.waiters.Len() >= p.MaxWaiters {
		p.removeCanceledFuturesLocked()
		if p.waiters.Len() >= p.MaxWaiters {
			p.waitersMu.Unlock()
			p.readyMu.Unlock()
			return zero, ErrMaxWaiters
		}
	}

	// Get in line before we start any loading to ensure FIFO order.
	ch, _ := p.chanPool.Get().(chan T)
	if ch == nil {
		ch = make(chan T, 1)
	}
	waiter := &waiter[T]{
		ch: ch,
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

// TryTake returns the next ready item, if any. It never waits or creates an
// item.
func (p *List[T]) TryTake() (_ T, ok bool) {
	p.readyMu.Lock()
	defer p.readyMu.Unlock()
	return p.ready.Pop()
}

// Put returns v to the List. It hands v to the longest-waiting caller, or
// stores it for later reuse. Put returns false after [List.Close].
func (p *List[T]) Put(v T) (accepted bool) {
	if p.closed.Load() {
		return false
	}

	maybeHandoff := func(v T) bool {
		p.waitersMu.Lock()
		defer p.waitersMu.Unlock()
		for {
			waiter, ok := p.waiters.Shift()
			if !ok {
				return false
			}
			if waiter.future != nil {
				if waiter.ctx.Err() != nil {
					var zero T
					p.finishFutureLocked(waiter, zero, context.Cause(waiter.ctx))
					continue
				}
				p.finishFutureLocked(waiter, v, nil)
				return true
			}
			select {
			case waiter.ch <- v:
			default:
				panic("waiter: waiter channel full (this is a bug in waitlist)")
			}
			return true
		}
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

// Retire removes one checked-out item from the live item count. If the List
// has a waiter and is open, Retire starts a replacement load. It does nothing
// when there are no live items.
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
	case v, ok := <-w.ch:
		// Near miss: a value arrived just as we were canceling.
		// Return it instead of the error.
		if ok {
			return v, nil
		}
	default:
	}
	return zero, errUnlessMissed
}

// finishFutureLocked resolves a reservation. The waiter has already been
// removed from the queue, or is about to be removed by DeleteFunc.
func (p *List[T]) finishFutureLocked(w *waiter[T], v T, err error) {
	if w.done {
		return
	}
	w.done = true
	if w.stop != nil {
		w.stop()
	}
	w.ctx = nil
	w.future.resolve(v, err)
}

func (p *List[T]) cancelFuture(w *waiter[T]) {
	p.waitersMu.Lock()
	defer p.waitersMu.Unlock()
	if w.done {
		return
	}
	err := context.Cause(w.ctx)
	p.waiters.DeleteFunc(func(queued *waiter[T]) bool { return queued == w })
	var zero T
	p.finishFutureLocked(w, zero, err)
}

// removeCanceledFuturesLocked frees capacity even if cancellation callbacks
// have not yet run.
func (p *List[T]) removeCanceledFuturesLocked() {
	p.waiters.DeleteFunc(func(w *waiter[T]) bool {
		if w.future == nil || w.ctx.Err() == nil {
			return false
		}
		var zero T
		p.finishFutureLocked(w, zero, context.Cause(w.ctx))
		return true
	})
}

func normalizeNew[T any](new func() T) func() T {
	if new != nil {
		return new
	}
	return func() (zero T) { return }
}

func (p *List[T]) startNextLoadLocked() {
	if !p.canLoadLocked() {
		return
	}

	waiter := p.nextWaiterToLoadLocked()
	if waiter == nil {
		return
	}
	waiter.loading = true
	p.loads++
	newItem := normalizeNew(p.New)
	go func() { p.Put(newItem()) }()
}

func (p *List[T]) canLoadLocked() bool {
	return p.MaxItems == 0 || p.loads < p.MaxItems
}

// nextWaiterToLoadLocked returns the next waiter that is not already loading
// an item, or nil if none.
func (p *List[T]) nextWaiterToLoadLocked() *waiter[T] {
	for waiter := range p.waiters.Values() {
		if !waiter.loading && (waiter.future == nil || waiter.ctx.Err() == nil) {
			return waiter
		}
	}
	return nil
}
