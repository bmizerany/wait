// Package wait provides a first-come line for reusable items.
//
// A [List] pools items added with [List.Add]. Queued callers get items in
// arrival order, so waiting is fair, and unused items wait in a LIFO stack,
// so the next caller gets the most recently used item, whose connection or
// cache is likeliest to still be warm.
//
// Take returns a [Ticket] holding the caller's place in line. Its Value waits
// for admission, and its Done gives the item back:
//
//	t := conns.Take(ctx)
//	defer t.Done()
//	c, err := t.Value()
//	if err != nil {
//		return err
//	}
//
// To keep the item instead, or replace a broken one with [List.Add], call
// [Ticket.Leave] before the deferred Done.
//
// A List of one item is a fair lock: holding the item is your turn. The
// package's demand example shares a link's connections and bandwidth
// among downloads with two such Lists, serving callers strictly in
// arrival order.
//
// Unlike a buffered channel, a List hands out the most recently returned
// item rather than the one idle longest, holds a caller's place in line
// without blocking, and takes each item back only once. Use [sync.Pool] for
// temporary allocation reuse, not for a bounded resource pool.
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

	// ErrDone is returned by [Ticket.Value] after [Ticket.Done], after
	// [Ticket.Leave] takes a waiting Ticket out of the line, and for the
	// zero Ticket.
	ErrDone = errors.New("ticket done")
)

// List pools reusable items of type Item.
//
// It hands items added with [List.Add] to callers of [List.Take] in arrival
// order: a returned item goes straight to the caller that has waited
// longest, never to one that arrives later. Unused items wait in a LIFO
// stack, so the next caller gets the most recently
// used, warmest item. A caller gives an item back with [Ticket.Done].
//
// A List never creates items, so an item that is dropped rather than
// given back or replaced with Add is gone for good; drop them all, and every
// Ticket's Value waits until its ctx is done. To dial ahead, or replace a
// broken connection, add items that do it themselves, as the package's
// redial example shows.
//
// The zero List is empty and ready to use. List is safe for concurrent
// use.
type List[Item any] struct {
	// MaxWaiters is the maximum number of Tickets waiting in line. A Take
	// that would exceed it returns a Ticket whose Value reports
	// ErrMaxWaiters. A Ticket whose ctx is done stops counting once the
	// List notices: when its Value returns, or when it reaches the front
	// of the line. Zero means no limit.
	MaxWaiters int

	mu      sync.Mutex       // guards the fields below
	ready   queue.Lifo[Item] // LIFO stack of ready items
	waiters line[Item]
	closed  bool
}

// Take returns a Ticket for an item. If ctx is done, the Ticket fails with
// ctx's cause, even if an item is ready or the List is closed. Otherwise,
// if an item is ready, the Ticket holds it already; if the List is closed,
// the Ticket fails with [ErrClosed]; and if neither, the Ticket waits in
// line. Take never blocks; the Ticket's Value waits. To take only a ready
// item, use [List.TryTake].
func (l *List[T]) Take(ctx context.Context) Ticket[T] {
	if ctx.Err() != nil {
		return Ticket[T]{err: context.Cause(ctx)}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if v, ok := l.ready.Pop(); ok {
		return l.waiters.ticket(l, ctx, v)
	}
	if l.closed {
		return Ticket[T]{err: ErrClosed}
	}
	var zero T
	t, err := l.waiters.join(l, ctx, zero, l.MaxWaiters)
	if err != nil {
		return Ticket[T]{err: err}
	}
	return t
}

// TryTake returns a Ticket holding the most recently used ready item, and
// true, or the zero Ticket and false if no item is ready. Unlike Take, it
// never joins the line. A List never leaves an item ready while a Ticket
// waits in line, so an item TryTake finds is owed to no one: TryTake
// cannot cut the line.
func (l *List[T]) TryTake() (Ticket[T], bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, ok := l.ready.Pop()
	if !ok {
		return Ticket[T]{}, false
	}
	return l.waiters.ticket(l, context.Background(), v), true
}

// Add adds v to the List as a new item. It hands v to the longest-waiting
// Ticket, or stores it for later. Add returns false after [List.Close].
// To give back an item taken from the List, use [Ticket.Done] instead.
func (l *List[T]) Add(v T) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	l.give(v)
	return true
}

// Close fails every waiting Ticket with [ErrClosed] and makes later
// [List.Add] calls return false. After Close, [List.Take] returns the
// remaining ready items, including any given back later, then fails with
// ErrClosed. Close is idempotent.
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
