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

	// readyMu guards ready and loads.
	readyMu sync.Mutex
	ready   queue.Lifo[Item] // LIFO stack of ready items
	loads   int              // number of live items tracked by the list

	waitersMu sync.Mutex // guards waiters
	waiters   line[listWaiter, Item]

	closed atomic.Bool
}

type listWaiter struct {
	loading bool // an item is being created on this waiter's behalf
}

// Close wakes pending callers with [ErrClosed]. Callers can still drain ready
// items with [List.Take], [List.Reserve], or [List.TryTake]. Later [List.Put]
// calls return false. Close is idempotent.
func (p *List[T]) Close() {
	if p.closed.Swap(true) {
		return
	}
	p.waitersMu.Lock()
	defer p.waitersMu.Unlock()
	p.waiters.close()
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
	f := newFuture[T]()
	p.readyMu.Lock()
	defer p.readyMu.Unlock()
	if v, ok := p.ready.Pop(); ok {
		f.resolve(v, nil)
		return f, nil
	}
	w, err := p.joinLocked(ctx, f)
	if err != nil {
		return nil, err
	}
	w.stop = context.AfterFunc(ctx, func() {
		p.waitersMu.Lock()
		defer p.waitersMu.Unlock()
		p.waiters.cancel(w)
	})
	p.waitersMu.Unlock()
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
	// A ready item wins even if the List is closed or ctx is done,
	// so ready items drain after Close, like a channel.
	if v, ok := p.ready.Pop(); ok {
		p.readyMu.Unlock()
		return v, nil
	}
	w, err := p.joinLocked(ctx, nil)
	p.readyMu.Unlock()
	if err != nil {
		return zero, err
	}
	p.waitersMu.Unlock()

	// A value that beats cancellation is the caller's, not an error.
	r, _ := p.waiters.wait(&p.waitersMu, w)
	return r.v, r.err
}

// joinLocked puts a new waiter at the end of the line and starts a load for
// it if the List may create one. The caller holds readyMu. On success,
// joinLocked returns with waitersMu held, so the caller can finish setting
// up the waiter before anything delivers to it.
func (p *List[T]) joinLocked(ctx context.Context, f *Future[T]) (*waiter[listWaiter, T], error) {
	if p.closed.Load() {
		return nil, ErrClosed
	}
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	p.waitersMu.Lock()
	// Close may have started while we acquired waitersMu.
	if p.closed.Load() {
		p.waitersMu.Unlock()
		return nil, ErrClosed
	}
	w, err := p.waiters.join(ctx, listWaiter{}, f, p.MaxWaiters)
	if err != nil {
		p.waitersMu.Unlock()
		return nil, err
	}
	if ctx.Err() == nil {
		p.startNextLoadLocked()
	}
	return w, nil
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

	// Attempt a handoff without locking readyMu to fast-path high-load
	// cases.
	if p.handoff(v) {
		return true
	}

	p.readyMu.Lock()
	defer p.readyMu.Unlock()
	if p.closed.Load() {
		return false
	}
	// Waiters may have arrived while we acquired readyMu.
	if p.handoff(v) {
		return true
	}
	p.ready.Push(v)
	return true
}

// handoff gives v to the first waiter that can take it and reports whether
// there was one. A canceled reservation cannot: its caller may never call
// Wait, so handoff finishes it with its cause and moves on. A canceled Take
// can; it returns v instead of the cause.
func (p *List[T]) handoff(v T) bool {
	p.waitersMu.Lock()
	defer p.waitersMu.Unlock()
	for {
		w, ok := p.waiters.front()
		if !ok {
			return false
		}
		if w.future == nil || w.ctx.Err() == nil {
			p.waiters.pop(v, nil)
			return true
		}
		var zero T
		p.waiters.pop(zero, context.Cause(w.ctx))
	}
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
	waiter.d.loading = true
	p.loads++
	newItem := normalizeNew(p.New)
	go func() { p.Put(newItem()) }()
}

func (p *List[T]) canLoadLocked() bool {
	return p.MaxItems == 0 || p.loads < p.MaxItems
}

// nextWaiterToLoadLocked returns the next waiter that is not already loading
// an item, or nil if none.
func (p *List[T]) nextWaiterToLoadLocked() *waiter[listWaiter, T] {
	for waiter := range p.waiters.q.Values() {
		if !waiter.d.loading && (waiter.future == nil || waiter.ctx.Err() == nil) {
			return waiter
		}
	}
	return nil
}
