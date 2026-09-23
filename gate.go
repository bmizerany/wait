package wait

import (
	"context"
	"sync"

	"blake.io/wait/queue"
)

// A Gate orders access to capacity tracked by the caller.
//
// It owns no items. Waiters are admitted strictly in arrival order, so a
// demand at the front can hold smaller demands behind it. [Gate.Fill] and
// [Gate.Refill] account for the caller's capacity.
//
// The zero value admits every demand. Gate is safe for concurrent use.
type Gate[D any] struct {
	// Fill reports whether d fits and reserves its capacity when returning
	// true. The Gate calls Fill with its lock held, only for the head waiter
	// or a lone caller. Fill must not block or call back into the Gate. A nil
	// Fill admits every demand.
	Fill func(d D) bool

	// Refill returns d's capacity to the caller's accounting. The Gate calls
	// it with its lock held before admitting queued demands. Refill must not
	// block or call back into the Gate. A nil Refill does nothing.
	Refill func(d D)

	mu      sync.Mutex
	waiters queue.Fifo[*gateWaiter[D]]
	closed  bool

	chanPool sync.Pool // of chan error

	testHookGateWaiterCanceled func() // runs at the top of handleCancel
}

// A gateWaiter's channel receives exactly one signal — nil for
// admitted, ErrClosed for closed — sent while holding the Gate's lock,
// in the same critical section that pops the waiter from the line.
// Cancellation deletes the waiter under the same lock, so a waiter is
// popped or deleted, never both, and a canceled waiter that finds
// itself already popped knows its signal has already arrived. Every
// channel is therefore drained before returning to the pool.
type gateWaiter[D any] struct {
	d  D
	ch chan error
}

// signal hands w its one signal. Callers hold the Gate's lock and pop
// w from the line in the same critical section, so the channel has
// room; a full channel means the gateWaiter invariant was broken.
func (w *gateWaiter[D]) signal(err error) {
	select {
	case w.ch <- err:
	default:
		panic("wait: gate waiter signaled twice (this is a bug in Gate)")
	}
}

// Wait queues d until it is admitted, ctx is done, or the Gate is closed.
//
// If the line is empty and Fill accepts d, Wait admits it immediately.
// Wait returns [ErrClosed] if the Gate is closed and the context cause if
// ctx is done first. If cancellation races with admission, Wait refunds d
// through Refill before returning the context cause.
func (l *Gate[D]) Wait(ctx context.Context, d D) error {
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
	w := &gateWaiter[D]{d: d, ch: ch}
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

// TryWait admits d only when no one is waiting and Fill accepts it. It
// returns false when d cannot be admitted or after [Gate.Close].
func (l *Gate[D]) TryWait(d D) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.waiters.Len() > 0 {
		return false
	}
	return l.fill(d)
}

// Put returns d's capacity through Refill, then admits queued demands while
// Fill accepts them. Put may admit several waiters and works after Close.
func (l *Gate[D]) Put(d D) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refill(d)
	l.admitLocked()
}

// Close wakes queued callers. Later [Gate.Wait] calls return [ErrClosed], and
// [Gate.TryWait] returns false. Close is idempotent.
func (l *Gate[D]) Close() {
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
// gateWaiter): an ErrClosed signal needs nothing back, but an
// admission that raced the cancellation is refunded so the caller can
// trust that a canceled Wait holds nothing.
func (l *Gate[D]) handleCancel(w *gateWaiter[D], cause error) error {
	if l.testHookGateWaiterCanceled != nil {
		l.testHookGateWaiterCanceled()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	head, _ := l.waiters.Front()
	n := l.waiters.Len()
	l.waiters.DeleteFunc(func(queued *gateWaiter[D]) bool {
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
		panic("wait: gate waiter popped but never signaled (this is a bug in Gate)")
	}
	return cause
}

// admitLocked pops and admits waiters from the head of the line while
// Fill accepts them. It upholds the Gate invariant: no demand is
// offered to Fill while another is ahead of it in line.
func (l *Gate[D]) admitLocked() {
	for {
		w, ok := l.waiters.Front()
		if !ok || !l.fill(w.d) {
			return
		}
		l.waiters.Shift()
		w.signal(nil)
	}
}

func (l *Gate[D]) fill(d D) bool {
	return l.Fill == nil || l.Fill(d)
}

func (l *Gate[D]) refill(d D) {
	if l.Refill != nil {
		l.Refill(d)
	}
}
