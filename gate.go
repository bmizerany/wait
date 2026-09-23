package wait

import (
	"context"
	"sync"
)

// A Gate admits demands in arrival order against capacity the caller
// keeps track of. The Gate stores nothing itself: [Gate.Claim] and
// [Gate.Release] move capacity in and out of the caller's own accounting.
//
// Admission is strictly first-come, so a demand at the front of the line
// can hold smaller demands behind it.
//
// The zero Gate admits every demand. Gate is safe for concurrent use.
type Gate[D any] struct {
	// Claim takes d's share of capacity and reports true, or takes nothing
	// and reports false if d does not fit yet. The Gate calls Claim only
	// for the demand at the front of the line, with its lock held, so the
	// accounting needs no lock of its own. Claim must not block or call
	// back into the Gate. A nil Claim admits every demand.
	Claim func(d D) bool

	// Release gives back the capacity a successful Claim(d) took. The Gate
	// calls Release at most once for each successful Claim, with its lock
	// held, and then admits waiters that now fit. Release must not block
	// or call back into the Gate. A nil Release does nothing.
	Release func(d D)

	mu      sync.Mutex // guards waiters, closed, and the d and gen of every slot
	waiters line[D, Grant[D]]
	closed  bool
	slots   sync.Pool // of *slot[D] that no Grant holds
}

// A Grant is a demand a [Gate] has admitted. It holds the demand's
// capacity until Release gives it back. Only the first Release of a Grant,
// or of any copy of it, has any effect, and Release works after Close. The
// zero Grant holds nothing.
type Grant[D any] struct {
	s   *slot[D]
	gen uint64
}

// A slot holds the demand behind a Grant. Release bumps gen, so a Grant
// released before, or a copy of one, no longer matches its slot and
// releases nothing, even after the Gate reuses the slot for a new Grant.
type slot[D any] struct {
	gate *Gate[D]
	d    D
	gen  uint64
}

// Wait queues d until it is admitted, ctx is done, or the Gate is closed.
// Once d is admitted, Wait returns a [Grant] holding d's capacity. The
// caller must call its Release, or d holds its capacity forever.
//
// If the line is empty and Claim takes d, Wait returns at once. Wait
// returns [ErrClosed] if the Gate is closed and the context cause if ctx
// is done first. A Wait that returns an error returns the zero Grant and
// holds nothing: if d was admitted as ctx was canceled, Wait releases it
// before returning.
func (g *Gate[D]) Wait(ctx context.Context, d D) (Grant[D], error) {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return Grant[D]{}, ErrClosed
	}
	if ctx.Err() != nil {
		g.mu.Unlock()
		return Grant[D]{}, context.Cause(ctx)
	}
	if _, ok := g.waiters.front(); !ok && g.claim(d) {
		gr := g.grantLocked(d)
		g.mu.Unlock()
		return gr, nil
	}
	w, _ := g.waiters.join(ctx, d, nil, 0)
	g.mu.Unlock()

	r, canceled := g.waiters.wait(&g.mu, w)
	if !canceled {
		return r.v, r.err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	// If d was admitted just as we were canceling, r.v holds its Grant:
	// give it back, so a canceled Wait holds nothing. Otherwise r.v is
	// the zero Grant, and this does nothing.
	r.v.releaseLocked()
	// w may have been the head; its successor may fit now.
	g.admitLocked()
	return Grant[D]{}, context.Cause(ctx)
}

// TryWait admits d only when no one is waiting and Claim takes it. On
// admission it returns a [Grant] holding d's capacity, as [Gate.Wait]
// does, and true. It returns the zero Grant and false when d cannot be
// admitted or after [Gate.Close].
func (g *Gate[D]) TryWait(d D) (Grant[D], bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, queued := g.waiters.front(); g.closed || queued || !g.claim(d) {
		return Grant[D]{}, false
	}
	return g.grantLocked(d), true
}

// grantLocked returns a Grant for the admitted d, reusing a free slot if
// the Gate has one.
func (g *Gate[D]) grantLocked(d D) Grant[D] {
	s, _ := g.slots.Get().(*slot[D])
	if s == nil {
		s = &slot[D]{gate: g}
	}
	s.d = d
	return Grant[D]{s: s, gen: s.gen}
}

// Release gives the grant's capacity back through [Gate.Release] and
// admits waiters that now fit.
func (g Grant[D]) Release() {
	if g.s == nil {
		return
	}
	gate := g.s.gate
	gate.mu.Lock()
	defer gate.mu.Unlock()
	g.releaseLocked()
}

func (g Grant[D]) releaseLocked() {
	s := g.s
	if s == nil || s.gen != g.gen {
		return
	}
	s.gen++
	d := s.d
	var zero D
	s.d = zero // don't keep d alive in a free slot
	s.gate.slots.Put(s)
	s.gate.release(d)
	s.gate.admitLocked()
}

// Close wakes queued callers. Later [Gate.Wait] calls return [ErrClosed], and
// [Gate.TryWait] returns false. Close is idempotent.
func (g *Gate[D]) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	g.waiters.close()
}

// admitLocked admits waiters from the head of the line while Claim takes
// them. No demand is offered to Claim while another is ahead of it in line.
func (g *Gate[D]) admitLocked() {
	for {
		w, ok := g.waiters.front()
		if !ok || !g.claim(w.d) {
			return
		}
		g.waiters.pop(g.grantLocked(w.d), nil)
	}
}

func (g *Gate[D]) claim(d D) bool {
	return g.Claim == nil || g.Claim(d)
}

func (g *Gate[D]) release(d D) {
	if g.Release != nil {
		g.Release(d)
	}
}
