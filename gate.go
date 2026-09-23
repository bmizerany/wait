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

	mu      sync.Mutex // guards waiters and closed
	waiters line[D]
	closed  bool
}

// Take returns a Ticket for demand d. If no one is waiting and Claim takes
// d, the Ticket is admitted already; otherwise it waits in line. Take
// never blocks; the Ticket's Value waits, and returns d once admitted.
func (g *Gate[D]) Take(ctx context.Context, d D) Ticket[D] {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return Ticket[D]{err: ErrClosed}
	}
	if ctx.Err() != nil {
		return Ticket[D]{err: context.Cause(ctx)}
	}
	if g.waiters.q.Len() > 0 {
		// Canceled waiters may still be in line, holding up the head.
		g.admitLocked()
	}
	if g.waiters.q.Len() == 0 && g.claim(d) {
		return g.waiters.ticket(g, d)
	}
	t, _ := g.waiters.join(g, ctx, d, 0)
	return t
}

// TryTake returns an admitted Ticket for d, and true, only when no one is
// waiting and Claim takes d. It returns the zero Ticket and false when d
// cannot be admitted at once or after [Gate.Close].
func (g *Gate[D]) TryTake(d D) (Ticket[D], bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return Ticket[D]{}, false
	}
	if g.waiters.q.Len() > 0 {
		g.admitLocked()
	}
	if g.waiters.q.Len() > 0 || !g.claim(d) {
		return Ticket[D]{}, false
	}
	return g.waiters.ticket(g, d), true
}

// Close fails waiting Tickets with [ErrClosed]. Later [Gate.Take] calls
// return failed Tickets, and [Gate.TryTake] returns false. Releasing a
// Ticket after Close still gives its capacity back. Close is idempotent.
func (g *Gate[D]) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	g.waiters.close()
}

func (g *Gate[D]) mutex() *sync.Mutex { return &g.mu }
func (g *Gate[D]) line() *line[D]     { return &g.waiters }
func (g *Gate[D]) retire(D)           {}

// give releases d's capacity and admits waiters that now fit.
func (g *Gate[D]) give(d D) {
	g.release(d)
	g.admitLocked()
}

// left admits waiters that fit now that one ahead of them has left.
func (g *Gate[D]) left() { g.admitLocked() }

// admitLocked admits waiters from the head of the line while Claim takes
// them. No demand is offered to Claim while another is ahead of it in line.
func (g *Gate[D]) admitLocked() {
	for {
		w, ok := g.waiters.front()
		if !ok || !g.claim(w.v) {
			return
		}
		g.waiters.admit(w.v)
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
