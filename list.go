// Package wait provides a first-come line for reusable items.
//
// A [List] pools items added with [List.Add]. Queued callers get items in
// arrival order, so waiting is fair, and unused items wait in a LIFO stack,
// so the next caller gets the most recently used item, whose connection or
// cache is likeliest to still be warm.
//
// Take returns a [Ticket] holding the caller's place in line. Its Value waits
// for admission, and its Release gives the item back:
//
//	t := conns.Take(ctx)
//	defer t.Release()
//	c, err := t.Value()
//	if err != nil {
//		return err
//	}
//
// A List of one item is a fair lock: holding the item is your turn. The
// package's VM example shares a host's CPUs and memory among VMs with two
// such Lists, serving callers strictly in arrival order.
//
// Use a buffered channel when FIFO order and warm reuse do not matter. Use [sync.Pool] for temporary allocation reuse, not for a bounded
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

	// ErrClosed is returned by [Ticket.Value] when the List is closed
	// before the Ticket is admitted.
	ErrClosed = errors.New("closed")

	// ErrReleased is returned by [Ticket.Value] after the Ticket is
	// released, and for the zero Ticket.
	ErrReleased = errors.New("ticket released")
)

// List pools reusable items of type Item.
//
// It hands items added with [List.Add] to callers in arrival order. Unused
// items wait in a LIFO stack, so the next caller gets the most recently
// used, warmest item. A caller gives an item back by releasing its [Ticket].
//
// A List never creates items. To create them lazily, keep them warm, or
// replace broken ones, add items that do it themselves: a slot holding a
// connection it dials in the background, say, as the package's slot
// example shows.
//
// The zero List is empty and ready to use. List is safe for concurrent
// use.
type List[Item any] struct {
	// MaxWaiters is the maximum number of Tickets waiting in line. A Take
	// that would exceed it returns a Ticket whose Value reports
	// ErrMaxWaiters. Zero means no limit.
	MaxWaiters int

	mu      sync.Mutex       // guards the fields below
	ready   queue.Lifo[Item] // LIFO stack of ready items
	waiters line[Item]
	closed  bool
}

// Take returns a Ticket for an item. If an item is ready, the Ticket holds it
// already, even if the List is closed or ctx is done, so ready items drain
// after Close like a channel. Otherwise the Ticket waits in line. Take
// never blocks; the Ticket's Value waits.
//
// To take only a ready item, pass a ctx that is already done: the Ticket
// then holds a ready item or fails with ctx's cause, without joining the
// line.
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
	return t
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

// Close fails waiting Tickets with [ErrClosed]. After Close, [List.Take]
// never waits, whatever its ctx: it returns a ready item while any remain,
// then a Ticket failed with ErrClosed. Items given back by releasing their
// Tickets after Close join the ready items. Later [List.Add] calls return
// false. Close is idempotent.
func (l *List[T]) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	l.waiters.close()
}

// give hands v to the first waiting Ticket, or stores it for later.
func (l *List[T]) give(v T) {
	if _, ok := l.waiters.front(); ok {
		l.waiters.admit(v)
		return
	}
	l.ready.Push(v)
}
