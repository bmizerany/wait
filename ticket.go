package wait

import "context"

// A Ticket is a place in the line of a [List] or a [Gate] and, once
// admitted, what it was admitted to: an item from a List, or a demand's
// capacity from a Gate.
//
// Value waits for admission. Release gives back what the Ticket holds, or
// leaves the line if the Ticket is still waiting; Retire ends the Ticket
// without giving anything back. The first Release or Retire ends the
// Ticket and every copy of it: after that, Value returns [ErrReleased],
// and Release and Retire do nothing. The zero Ticket is ended.
//
// A Ticket is for use by one goroutine at a time.
type Ticket[T any] struct {
	w   *waiter[T]
	gen uint64
	err error // why the Ticket failed without joining the line
}

// Value returns the item or demand the Ticket was admitted with, waiting
// for admission if necessary. If the Ticket failed, Value returns the
// error: [ErrClosed], [ErrMaxWaiters], or the cause of the context passed
// to Take. An admission that races with cancellation wins.
func (t Ticket[T]) Value() (T, error) {
	var zero T
	w := t.w
	if w == nil {
		if t.err != nil {
			return zero, t.err
		}
		return zero, ErrReleased
	}
	if st := w.state(); w.gen.Load() != t.gen {
		return zero, ErrReleased
	} else if st != waiting {
		// Settled: only this Ticket's holder touches w now.
		return w.v, w.err
	}
	mu := w.o.mutex()
	mu.Lock()
	defer mu.Unlock()
	if w.state() == waiting {
		mu.Unlock()
		select {
		case <-w.ch:
		case <-w.ctx.Done():
			if h := w.o.line().testHookCanceled; h != nil {
				h()
			}
		}
		mu.Lock()
		if w.gen.Load() != t.gen {
			return zero, ErrReleased
		}
		if w.state() == waiting && w.o.line().leave(w) {
			w.fail(context.Cause(w.ctx))
			w.o.left()
		}
		select {
		case <-w.ch:
		default:
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
	mu := w.o.mutex()
	mu.Lock()
	defer mu.Unlock()
	return w.state() != waiting || w.ctx.Err() != nil
}

// Release ends the Ticket. If it was admitted, Release gives its item back
// to the List, or its capacity back to the Gate; if it is still waiting,
// Release takes it out of the line.
func (t Ticket[T]) Release() { t.end(false) }

// Retire ends the Ticket without giving back what it holds. A List no
// longer counts a retired item among its live items, and may create a
// replacement; a Gate never releases a retired demand's capacity.
func (t Ticket[T]) Retire() { t.end(true) }

func (t Ticket[T]) end(retire bool) {
	w := t.w
	if w == nil {
		return
	}
	mu := w.o.mutex()
	mu.Lock()
	defer mu.Unlock()
	if w.gen.Load() != t.gen {
		return
	}
	switch w.state() {
	case waiting:
		w.o.line().leave(w)
		w.o.line().recycle(w)
		w.o.left()
	case admitted:
		v := w.v
		w.o.line().recycle(w)
		if retire {
			w.o.retire(v)
		} else {
			w.o.give(v)
		}
	case failed:
		w.o.line().recycle(w)
	}
}
