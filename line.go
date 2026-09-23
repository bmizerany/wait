package wait

import (
	"context"
	"sync"
	"sync/atomic"

	"blake.io/wait/queue"
)

// An owner is the List or Gate behind a line and its Tickets. Every
// method but mutex runs with the mutex held.
type owner[V any] interface {
	mutex() *sync.Mutex // guards the line and the state of its waiters
	line() *line[V]
	give(v V)   // take back the value of an admitted Ticket
	retire(v V) // end an admitted Ticket without taking its value back
	left()      // a waiter left the line before admission
}

// A line is the first-come queue behind a List or a Gate, and the pool of
// waiters behind their Tickets. The owner's mutex guards it.
type line[V any] struct {
	q     queue.Fifo[*waiter[V]]
	spare [4]*waiter[V] // recycled waiters kept for reuse
	nfree int           // spare[:nfree] are free
	free  sync.Pool     // of *waiter[V] beyond spare; empties at GC

	testHookCanceled func() // runs in Value when ctx is done first
}

type state uint32

const (
	waiting  state = iota // in the line
	admitted              // holds v
	failed                // holds err
)

// A waiter backs a Ticket. It is waiting in the line, then admitted or
// failed. Leaving the line and settling happen in one critical section,
// so a waiter is never both in the line and settled. Settling also puts
// a token in ch to wake a Value blocked on it.
//
// When its Ticket is released, the waiter's gen is bumped before it
// returns to the pool, so a stale copy of the Ticket no longer matches it.
//
// The owner's mutex guards every field, but st and gen are also atomic:
// once a waiter settles, only its Ticket's holder touches it, so Value can
// find it settled and read v and err without the mutex.
type waiter[V any] struct {
	o       owner[V]
	ch      chan struct{} // wakes Value; holds a token while settled and unread
	ctx     context.Context
	v       V // for a Gate, the demand; for a List, the admitted item
	err     error
	st      atomic.Uint32 // a state; stored after v and err
	gen     atomic.Uint64
	loading bool // a List is creating an item on this waiter's behalf
	queued  bool // joined the line, so ch may hold a token
}

func (w *waiter[V]) state() state { return state(w.st.Load()) }

func (l *line[V]) get(o owner[V]) *waiter[V] {
	if l.nfree > 0 {
		l.nfree--
		w := l.spare[l.nfree]
		l.spare[l.nfree] = nil
		return w
	}
	w, _ := l.free.Get().(*waiter[V])
	if w == nil {
		w = &waiter[V]{o: o, ch: make(chan struct{}, 1)}
	}
	return w
}

// ticket returns an already admitted Ticket for v.
func (l *line[V]) ticket(o owner[V], v V) Ticket[V] {
	w := l.get(o)
	w.v = v
	w.st.Store(uint32(admitted))
	return Ticket[V]{w: w, gen: w.gen.Load()}
}

// join adds a waiter for ctx, holding v, to the end of the line and returns
// its Ticket. If max is positive and the line holds max waiters even after
// removing those whose ctx is done, join returns ErrMaxWaiters instead.
func (l *line[V]) join(o owner[V], ctx context.Context, v V, max int) (Ticket[V], error) {
	if max > 0 && l.q.Len() >= max {
		l.prune()
		if l.q.Len() >= max {
			return Ticket[V]{}, ErrMaxWaiters
		}
	}
	w := l.get(o)
	w.ctx, w.v, w.queued = ctx, v, true
	w.st.Store(uint32(waiting))
	l.q.Unshift(w)
	return Ticket[V]{w: w, gen: w.gen.Load()}, nil
}

// front returns the first waiter whose ctx is not done, failing any ahead
// of it: a canceled waiter gives up its turn even if no one is waiting on
// its Ticket.
func (l *line[V]) front() (*waiter[V], bool) {
	for {
		w, ok := l.q.Front()
		if !ok || w.ctx.Err() == nil {
			return w, ok
		}
		l.q.Shift()
		w.fail(context.Cause(w.ctx))
	}
}

// admit removes the waiter at the front of the line and admits it with v.
func (l *line[V]) admit(v V) {
	if w, ok := l.q.Shift(); ok {
		w.v = v
		w.st.Store(uint32(admitted))
		w.wake()
	}
}

// leave removes w from the line and reports whether it was there.
func (l *line[V]) leave(w *waiter[V]) bool {
	n := l.q.Len()
	l.q.DeleteFunc(func(q *waiter[V]) bool { return q == w })
	return l.q.Len() < n
}

// prune fails every waiter whose ctx is done.
func (l *line[V]) prune() {
	l.q.DeleteFunc(func(w *waiter[V]) bool {
		if w.ctx.Err() == nil {
			return false
		}
		w.fail(context.Cause(w.ctx))
		return true
	})
}

// close empties the line, failing each waiter with ErrClosed, or with the
// cause of its ctx if that is already done.
func (l *line[V]) close() {
	for {
		w, ok := l.q.Shift()
		if !ok {
			return
		}
		if w.ctx.Err() != nil {
			w.fail(context.Cause(w.ctx))
		} else {
			w.fail(ErrClosed)
		}
	}
}

// recycle returns w to the pool once its Ticket is released. Bumping gen
// makes every copy of the Ticket stale.
func (l *line[V]) recycle(w *waiter[V]) {
	var zero V
	w.gen.Add(1)
	if w.queued {
		select {
		case <-w.ch:
		default:
		}
	}
	w.ctx, w.v, w.err, w.loading, w.queued = nil, zero, nil, false, false
	if l.nfree < len(l.spare) {
		l.spare[l.nfree] = w
		l.nfree++
		return
	}
	l.free.Put(w)
}

func (w *waiter[V]) fail(err error) {
	var zero V
	w.v, w.err = zero, err
	w.st.Store(uint32(failed))
	w.wake()
}

// wake signals a settled waiter. The owner settles a waiter once, as it
// leaves the line, so ch always has room.
func (w *waiter[V]) wake() {
	select {
	case w.ch <- struct{}{}:
	default:
		panic("wait: waiter settled twice (this is a bug in wait)")
	}
}
