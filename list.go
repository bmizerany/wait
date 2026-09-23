// Package wait provides first-come lines for reusable items and capacity.
//
// A [List] pools items added with [List.Add] and can create more lazily up to a
// limit. Queued callers get items in arrival order, so waiting is fair, and
// unused items wait in a LIFO stack, so the next caller gets the most recently
// used item, whose connection or cache is likeliest to still be warm.
//
// A [Gate] stores no items. It admits demands in strict arrival order against
// capacity the caller keeps track of: [Gate.Claim] takes a demand's share,
// and [Gate.Release] gives it back.
//
// Take on either returns a [Ticket] holding the caller's place in line. Its
// Value waits for admission, and its Release gives back what it holds:
//
//	t := conns.Take(ctx)
//	defer t.Release()
//	c, err := t.Value()
//	if err != nil {
//		return err
//	}
//
// Use a buffered channel when FIFO order and lazy creation limits are not
// needed. Use [sync.Pool] for temporary allocation reuse, not for a bounded
// resource pool.
package wait

import (
	"context"
	"errors"
	"sync"

	"blake.io/wait/queue"
)

var (
	// ErrMaxWaiters is returned by [Ticket.Value] when [List.Take] found
	// MaxWaiters Tickets already waiting.
	ErrMaxWaiters = errors.New("too many waiters")

	// ErrClosed is returned by [Ticket.Value] when the List or Gate is
	// closed before the Ticket is admitted.
	ErrClosed = errors.New("closed")

	// ErrReleased is returned by [Ticket.Value] after the Ticket is
	// released or retired, and for the zero Ticket.
	ErrReleased = errors.New("ticket released")
)

// List pools reusable items of type Item.
//
// It pools items added with [List.Add] and can create more lazily up to
// MaxItems. Queued callers receive items in FIFO order; unused items wait in a
// LIFO stack, so the next caller gets the most recently used, warmest item.
// A caller gives back an item by releasing its [Ticket], or drops it for good
// by retiring the Ticket.
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

	// MaxWaiters is the maximum number of Tickets waiting in line. A Take
	// that would exceed it returns a Ticket whose Value reports
	// ErrMaxWaiters. Zero means no limit.
	MaxWaiters int

	mu      sync.Mutex       // guards the fields below
	ready   queue.Lifo[Item] // LIFO stack of ready items
	loads   int              // number of live items created by New
	waiters line[Item]
	closed  bool
}

// Take returns a Ticket for an item. If an item is ready, the Ticket holds it
// already, even if the List is closed or ctx is done, so ready items drain
// after Close like a channel. Otherwise the Ticket waits in line, and Take
// starts creating an item if MaxItems allows. Take never blocks; the
// Ticket's Value waits.
func (l *List[T]) Take(ctx context.Context) Ticket[T] {
	l.mu.Lock()
	defer l.mu.Unlock()
	if v, ok := l.ready.Pop(); ok {
		return l.waiters.ticket(l, v)
	}
	if l.closed {
		return Ticket[T]{err: ErrClosed}
	}
	if ctx.Err() != nil {
		return Ticket[T]{err: context.Cause(ctx)}
	}
	var zero T
	t, err := l.waiters.join(l, ctx, zero, l.MaxWaiters)
	if err != nil {
		return Ticket[T]{err: err}
	}
	l.startNextLoadLocked()
	return t
}

// TryTake returns a Ticket holding the next ready item, if any. It never
// waits or creates an item.
func (l *List[T]) TryTake() (Ticket[T], bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, ok := l.ready.Pop()
	if !ok {
		return Ticket[T]{}, false
	}
	return l.waiters.ticket(l, v), true
}

// Add adds v to the List as a new item. It hands v to the longest-waiting
// Ticket, or stores it for later. Add returns false after [List.Close].
// To give back an item taken from the List, release its Ticket instead.
func (l *List[T]) Add(v T) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	l.give(v)
	return true
}

// Close fails waiting Tickets with [ErrClosed]. Ready items can still be
// drained with [List.Take] or [List.TryTake], and items given back by
// releasing their Tickets after Close join them. Later [List.Add] calls
// return false. Close is idempotent.
func (l *List[T]) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	l.waiters.close()
}

func (l *List[T]) mutex() *sync.Mutex { return &l.mu }
func (l *List[T]) line() *line[T]     { return &l.waiters }
func (l *List[T]) left()              {}

// give hands v to the first waiting Ticket, or stores it for later.
func (l *List[T]) give(v T) {
	if _, ok := l.waiters.front(); ok {
		l.waiters.admit(v)
		return
	}
	l.ready.Push(v)
}

// retire drops a retired item from the live count and, if the List is
// open, starts a replacement for a waiting Ticket.
func (l *List[T]) retire(T) {
	if l.loads == 0 {
		return
	}
	l.loads--
	if !l.closed {
		l.startNextLoadLocked()
	}
}

func normalizeNew[T any](new func() T) func() T {
	if new != nil {
		return new
	}
	return func() (zero T) { return }
}

func (l *List[T]) startNextLoadLocked() {
	if l.MaxItems > 0 && l.loads >= l.MaxItems {
		return
	}
	w := l.nextWaiterToLoadLocked()
	if w == nil {
		return
	}
	w.loading = true
	l.loads++
	newItem := normalizeNew(l.New)
	go func() { l.Add(newItem()) }()
}

// nextWaiterToLoadLocked returns the first waiting Ticket's waiter that
// no load is running for, or nil if none.
func (l *List[T]) nextWaiterToLoadLocked() *waiter[T] {
	for w := range l.waiters.q.Values() {
		if !w.loading && w.ctx.Err() == nil {
			return w
		}
	}
	return nil
}
