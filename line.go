package wait

import (
	"context"
	"sync"

	"blake.io/wait/queue"
)

// A Line admits demands in first-come order.
//
// Wait joins the line with a demand and blocks until the demand is
// admitted, ctx is done, or the Line is closed. Only the demand at the
// head of the line is ever offered to Fill; when Fill accepts, that
// waiter is admitted and the next head is offered. A demand behind the
// head is never admitted first, no matter how small — strict arrival
// order, intentional head-of-line blocking.
//
// Unlike [List], a Line carries no items. It orders admission to
// capacity the caller accounts for in Fill and Refill.
//
// The zero value is a usable Line that admits everything.
// It is safe for concurrent use.
type Line[D any] struct {
	// Fill reports whether d can be admitted now, deducting whatever
	// d needs from the caller's accounting when it returns true.
	// The Line calls Fill with its lock held, only ever for the
	// demand at the head of the line (or for a lone Wait or TryWait
	// caller when the line is empty), so the accounting needs no
	// lock of its own. Fill must not block or call back into the
	// Line. A nil Fill admits everything.
	Fill func(d D) bool

	// Refill returns d's capacity to the caller's accounting.
	// Put calls it with the Line's lock held, before offering the
	// head of the line to Fill again. A nil Refill is a no-op.
	Refill func(d D)

	mu      sync.Mutex
	waiters queue.Fifo[*lineWaiter[D]]
	closed  bool

	chanPool sync.Pool // of chan error

	testHookLineWaiterCanceled func() // runs at the top of handleCancel
}

// A lineWaiter's channel receives exactly one signal — nil for
// admitted, ErrClosed for closed — sent while holding the Line's lock,
// in the same critical section that pops the waiter from the line.
// Cancellation deletes the waiter under the same lock, so a waiter is
// popped or deleted, never both, and a canceled waiter that finds
// itself already popped knows its signal has already arrived. Every
// channel is therefore drained before returning to the pool.
type lineWaiter[D any] struct {
	d  D
	ch chan error
}

// signal hands w its one signal. Callers hold the Line's lock and pop
// w from the line in the same critical section, so the channel has
// room; a full channel means the lineWaiter invariant was broken.
func (w *lineWaiter[D]) signal(err error) {
	select {
	case w.ch <- err:
	default:
		panic("wait: line waiter signaled twice (this is a bug in Line)")
	}
}

// Wait joins the line with demand d and blocks until d is admitted,
// ctx is done, or the Line is closed.
//
// If the line is empty and Fill accepts d, Wait admits immediately
// without queueing. Wait returns nil once admitted, [ErrClosed] if the
// Line is closed, and the context cause if ctx is done first — even
// when an admission raced the cancellation: the raced grant is
// refunded via Refill, so a non-nil error means d holds nothing.
func (l *Line[D]) Wait(ctx context.Context, d D) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return ErrClosed
	}
	if ctx.Err() != nil {
		l.mu.Unlock()
		return context.Cause(ctx)
	}
	if l.waiters.Len() == 0 && l.fill(d) {
		l.mu.Unlock()
		return nil
	}

	ch, _ := l.chanPool.Get().(chan error)
	if ch == nil {
		ch = make(chan error, 1)
	}
	w := &lineWaiter[D]{d: d, ch: ch}
	l.waiters.Unshift(w)
	l.mu.Unlock()

	select {
	case err := <-w.ch:
		l.chanPool.Put(w.ch)
		return err
	case <-ctx.Done():
		err := l.handleCancel(w, context.Cause(ctx))
		l.chanPool.Put(w.ch)
		return err
	}
}

// TryWait admits d without waiting if the line is empty and Fill
// accepts it, and reports whether d was admitted. A TryWait caller
// never takes capacity ahead of anyone already in line.
// TryWait returns false after Close.
func (l *Line[D]) TryWait(d D) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.waiters.Len() > 0 {
		return false
	}
	return l.fill(d)
}

// Put returns d's capacity to the caller's accounting via Refill, then
// admits from the head of the line for as long as Fill accepts — one
// Put may admit several waiters. Put never blocks.
//
// Put works after Close: the refill still lands, since the accounting
// belongs to the caller; there is just no one left to admit.
func (l *Line[D]) Put(d D) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refill(d)
	l.admitLocked()
}

// Close closes the Line. Waiting goroutines are unblocked and receive
// [ErrClosed]; nothing is refunded, because a demand still in line
// never deducted anything. Wait and TryWait fail after Close.
// Close is idempotent.
func (l *Line[D]) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	for {
		w, ok := l.waiters.Shift()
		if !ok {
			return
		}
		w.signal(ErrClosed)
	}
}

// handleCancel removes w from the line and returns cause. If w is
// already gone from the line, it was popped and signaled (see
// lineWaiter): an ErrClosed signal needs nothing back, but an
// admission that raced the cancellation is refunded so the caller can
// trust that a canceled Wait holds nothing.
func (l *Line[D]) handleCancel(w *lineWaiter[D], cause error) error {
	if l.testHookLineWaiterCanceled != nil {
		l.testHookLineWaiterCanceled()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	head, _ := l.waiters.Front()
	n := l.waiters.Len()
	l.waiters.DeleteFunc(func(queued *lineWaiter[D]) bool {
		return w == queued
	})
	if l.waiters.Len() < n {
		if head == w {
			// w was the head; its successor may fit now.
			l.admitLocked()
		}
		return cause
	}

	select {
	case err := <-w.ch:
		if err == nil {
			// Near miss: admitted just as we were canceling.
			l.refill(w.d)
			l.admitLocked()
		}
	default:
		panic("wait: line waiter popped but never signaled (this is a bug in Line)")
	}
	return cause
}

// admitLocked pops and admits waiters from the head of the line while
// Fill accepts them. It upholds the Line invariant: no demand is
// offered to Fill while another is ahead of it in line.
func (l *Line[D]) admitLocked() {
	for {
		w, ok := l.waiters.Front()
		if !ok || !l.fill(w.d) {
			return
		}
		l.waiters.Shift()
		w.signal(nil)
	}
}

func (l *Line[D]) fill(d D) bool {
	return l.Fill == nil || l.Fill(d)
}

func (l *Line[D]) refill(d D) {
	if l.Refill != nil {
		l.Refill(d)
	}
}
