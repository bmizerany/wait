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
	// calls it with its lock held, from Put and when a canceled Wait turns
	// out to have been admitted, and then admits waiters that now fit.
	// Release must not block or call back into the Gate. A nil Release
	// does nothing.
	Release func(d D)

	mu      sync.Mutex // guards waiters and closed
	waiters line[D, struct{}]
	closed  bool
}

// Wait queues d until it is admitted, ctx is done, or the Gate is closed.
//
// If the line is empty and Claim takes d, Wait returns at once. Wait
// returns [ErrClosed] if the Gate is closed and the context cause if ctx
// is done first. A Wait that returns an error holds nothing: if d was
// admitted as ctx was canceled, Wait releases it before returning.
func (g *Gate[D]) Wait(ctx context.Context, d D) error {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return ErrClosed
	}
	if ctx.Err() != nil {
		g.mu.Unlock()
		return context.Cause(ctx)
	}
	if _, ok := g.waiters.front(); !ok && g.claim(d) {
		g.mu.Unlock()
		return nil
	}
	w, _ := g.waiters.join(ctx, d, nil, 0)
	g.mu.Unlock()

	r, canceled := g.waiters.wait(&g.mu, w)
	if !canceled {
		return r.err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.err == nil {
		// Admitted just as we were canceling: give it back, so a
		// canceled Wait holds nothing.
		g.release(d)
	}
	// w may have been the head; its successor may fit now.
	g.admitLocked()
	return context.Cause(ctx)
}

// TryWait admits d only when no one is waiting and Claim takes it. It
// returns false when d cannot be admitted or after [Gate.Close].
func (g *Gate[D]) TryWait(d D) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.waiters.front(); g.closed || ok {
		return false
	}
	return g.claim(d)
}

// Put gives back d's capacity through Release, then admits waiters from
// the front of the line while Claim takes them. Put may admit several
// waiters and works after Close.
func (g *Gate[D]) Put(d D) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.release(d)
	g.admitLocked()
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
		g.waiters.pop(struct{}{}, nil)
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
