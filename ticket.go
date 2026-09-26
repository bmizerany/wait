package wait

import "context"

// A Ticket is a place in a [List]'s line and, once admitted, the item it
// was admitted with.
//
// Value waits for admission. Done ends the Ticket, giving its item back
// to the List; call Done on every Ticket, with defer. To keep the item,
// or replace it with [List.Add], call Leave first: Done then gives
// nothing back. The first Done ends the Ticket and every copy of it:
// after that, Value returns [ErrReleased], and Leave and Done do
// nothing. A Ticket that failed in Take, without joining the line, has
// nothing to give back; its Value keeps returning the same error. The
// zero Ticket is ended.
//
// Once Value returns an item, the holder of a Ticket that is never
// done owns the item outright. A Ticket still waiting keeps its place
// until its ctx is done or it leaves.
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
// done.
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

// Leave takes the Ticket out of the line if it is still waiting, and
// reports whether this is the first Leave of the Ticket or its copies. An
// admitted Ticket has left the line already; Leave then keeps its item
// for its holder, and Done no longer gives it back. Once Leave returns
// true, Value never waits: it returns the item, or, if Leave took the
// Ticket out of the line, the cause of its ctx if done, else
// [ErrReleased]. Leave returns false after Done, for a Ticket that
// failed in Take, and for the zero Ticket.
func (t Ticket[T]) Leave() bool {
	w := t.w
	if w == nil {
		return false
	}
	l := w.list
	l.mu.Lock()
	defer l.mu.Unlock()
	if w.gen.Load() != t.gen || w.left {
		return false
	}
	w.left = true
	if w.state() == waiting {
		l.waiters.leave(w)
		err := ErrReleased
		if w.ctx.Err() != nil {
			err = context.Cause(w.ctx)
		}
		w.fail(err)
	}
	return true
}

// Done ends the Ticket and every copy of it. If the Ticket is still
// waiting, Done takes it out of the line; if it holds an item and has
// not left, Done gives the item back to the List.
func (t Ticket[T]) Done() {
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
	var v T
	give := false
	if !w.left {
		switch w.state() {
		case waiting:
			l.waiters.leave(w)
		case admitted, taken:
			v, give = w.v, true
		}
	}
	l.waiters.recycle(w)
	if give {
		l.give(v)
	}
}
