package wait

import "context"

// A Ticket is a place in a [List]'s line and, once admitted, the item it
// was admitted with.
//
// Value waits for admission. Release gives the item back to the List, or
// leaves the line if the Ticket is still waiting. The first Release ends
// the Ticket and every copy of it: after that, Value returns
// [ErrReleased], and Release does nothing. A Ticket that failed in Take,
// without joining the line, has nothing to release; its Value keeps
// returning the same error. The zero Ticket is ended.
//
// Once Value returns an item, the holder of a Ticket that is never
// released owns the item outright. A Ticket still waiting keeps its place
// until its ctx is done or it is released.
//
// A Ticket is for use by one goroutine at a time.
type Ticket[T any] struct {
	w   *waiter[T]
	gen uint64
	err error // why the Ticket failed without joining the line
}

// Value returns the item the Ticket was admitted with, waiting for
// admission if necessary. If the Ticket failed, Value returns the error:
// [ErrClosed], [ErrMaxWaiters], or the cause of the context passed to
// Take. Cancellation wins over admission: if the context is done before
// Value returns the item, even an item admitted first, Value gives the
// item back to the List and fails the Ticket with the context's cause.
// Once Value returns an item, it returns that item until the Ticket is
// released.
func (t Ticket[T]) Value() (T, error) {
	var zero T
	w := t.w
	if w == nil {
		if t.err != nil {
			return zero, t.err
		}
		return zero, ErrReleased
	}
	st := w.state()
	if w.gen.Load() != t.gen {
		return zero, ErrReleased
	}
	// Once settled, w is touched only by this Ticket's holder.
	switch st {
	case taken, failed:
		return w.v, w.err
	case admitted:
		if w.ctx.Err() == nil {
			w.st.Store(uint32(taken))
			return w.v, nil
		}
	}
	mu := &w.list.mu
	mu.Lock()
	defer mu.Unlock()
	if w.state() == waiting {
		mu.Unlock()
		select {
		case <-w.ch:
		case <-w.ctx.Done():
			if h := w.list.waiters.testHookCanceled; h != nil {
				h()
			}
		}
		mu.Lock()
		if w.gen.Load() != t.gen {
			return zero, ErrReleased
		}
		if w.state() == waiting && w.list.waiters.leave(w) {
			w.fail(context.Cause(w.ctx))
		}
		select {
		case <-w.ch:
		default:
		}
	}
	if w.state() == admitted {
		if w.ctx.Err() == nil {
			w.st.Store(uint32(taken))
		} else {
			// Not w.fail: this Value is the only one that could wait on
			// w, and admission may already have left a token in w.ch.
			v := w.v
			w.v, w.err = zero, context.Cause(w.ctx)
			w.st.Store(uint32(failed))
			w.list.give(v)
		}
	}
	return w.v, w.err
}

// Ready reports whether Value would return without waiting.
func (t Ticket[T]) Ready() bool {
	w := t.w
	if w == nil {
		return true
	}
	if w.gen.Load() != t.gen || w.state() != waiting {
		return true
	}
	mu := &w.list.mu
	mu.Lock()
	defer mu.Unlock()
	return w.state() != waiting || w.ctx.Err() != nil
}

// Release ends the Ticket. If it holds an item, Release gives the item
// back to the List; if it is still waiting, Release takes it out of the
// line.
func (t Ticket[T]) Release() {
	w := t.w
	if w == nil {
		return
	}
	l := w.list
	l.mu.Lock()
	defer l.mu.Unlock()
	if w.gen.Load() != t.gen {
		return
	}
	switch w.state() {
	case waiting:
		l.waiters.leave(w)
		l.waiters.recycle(w)
	case admitted, taken:
		v := w.v
		l.waiters.recycle(w)
		l.give(v)
	case failed:
		l.waiters.recycle(w)
	}
}
