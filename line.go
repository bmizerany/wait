package wait

import (
	"context"
	"sync"

	"blake.io/wait/queue"
)

// A line is the first-come queue of waiters behind a List or a Gate.
// The owner's lock guards the line; only wait runs without it.
//
// Each waiter receives exactly one result, delivered in the same critical
// section that removes it from the line. A waiter is either in the line
// or holding its result, never both, so a canceled waiter that finds
// itself already gone knows its result has arrived.
type line[D, V any] struct {
	q     queue.Fifo[*waiter[D, V]]
	chans sync.Pool // of chan result[V]

	testHookCanceled func() // runs in wait before leaving the line
}

type waiter[D, V any] struct {
	ctx    context.Context
	d      D              // the owner's per-waiter state
	ch     chan result[V] // result of a blocking call; nil for a reservation
	future *Future[V]     // result of a reservation
	stop   func() bool    // stops a reservation's cancellation callback
}

type result[V any] struct {
	v   V
	err error
}

// join adds a waiter for d to the end of the line and returns it. The
// waiter receives its result through f if f is non-nil and through a
// channel for wait otherwise. If max is positive and the line holds max
// waiters even after removing those whose ctx is done, join returns
// ErrMaxWaiters.
func (l *line[D, V]) join(ctx context.Context, d D, f *Future[V], max int) (*waiter[D, V], error) {
	if max > 0 && l.q.Len() >= max {
		l.prune()
		if l.q.Len() >= max {
			return nil, ErrMaxWaiters
		}
	}
	w := &waiter[D, V]{ctx: ctx, d: d, future: f}
	if f == nil {
		w.ch, _ = l.chans.Get().(chan result[V])
		if w.ch == nil {
			w.ch = make(chan result[V], 1)
		}
	}
	l.q.Unshift(w)
	return w, nil
}

func (l *line[D, V]) front() (*waiter[D, V], bool) {
	return l.q.Front()
}

// pop removes the waiter at the front of the line and delivers v and err
// to it.
func (l *line[D, V]) pop(v V, err error) {
	if w, ok := l.q.Shift(); ok {
		w.deliver(v, err)
	}
}

// cancel removes w from the line and delivers the cause of its ctx.
// If w has already left the line, it already has its result, and cancel
// does nothing.
func (l *line[D, V]) cancel(w *waiter[D, V]) {
	n := l.q.Len()
	l.q.DeleteFunc(func(q *waiter[D, V]) bool { return q == w })
	if l.q.Len() < n {
		var zero V
		w.deliver(zero, context.Cause(w.ctx))
	}
}

// prune cancels every waiter whose ctx is done, freeing its place even if
// the waiter has not yet noticed.
func (l *line[D, V]) prune() {
	l.q.DeleteFunc(func(w *waiter[D, V]) bool {
		if w.ctx.Err() == nil {
			return false
		}
		var zero V
		w.deliver(zero, context.Cause(w.ctx))
		return true
	})
}

// close empties the line, delivering ErrClosed, or the cause of a waiter's
// ctx if it is already done.
func (l *line[D, V]) close() {
	var zero V
	for {
		w, ok := l.q.Shift()
		if !ok {
			return
		}
		if w.ctx.Err() != nil {
			w.deliver(zero, context.Cause(w.ctx))
		} else {
			w.deliver(zero, ErrClosed)
		}
	}
}

// wait returns the result for w, which join created without a Future,
// blocking until it arrives or w's ctx is done. In the second case, wait
// takes w out of the line while holding mu and reports canceled. The
// result is then the cause of w's ctx, unless a value beat the
// cancellation, in which case the result holds that value and a nil error.
func (l *line[D, V]) wait(mu sync.Locker, w *waiter[D, V]) (r result[V], canceled bool) {
	select {
	case r = <-w.ch:
	case <-w.ctx.Done():
		if l.testHookCanceled != nil {
			l.testHookCanceled()
		}
		mu.Lock()
		l.cancel(w)
		mu.Unlock()
		r, canceled = <-w.ch, true
		if r.err != nil {
			r.err = context.Cause(w.ctx)
		}
	}
	l.chans.Put(w.ch)
	return r, canceled
}

// deliver hands w its one result. Callers remove w from the line in the
// same critical section, so the channel has room; a full channel means a
// second delivery, which the line invariant forbids.
func (w *waiter[D, V]) deliver(v V, err error) {
	if w.future != nil {
		if w.stop != nil {
			w.stop()
		}
		w.future.resolve(v, err)
		return
	}
	select {
	case w.ch <- result[V]{v, err}:
	default:
		panic("wait: waiter received two results (this is a bug in wait)")
	}
}
