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
	if _, ok := l.waiters.front(); !ok && l.claim(d) {
		l.mu.Unlock()
		return nil
	}
	w, _ := l.waiters.join(ctx, d, nil, 0)
	l.mu.Unlock()

	r, canceled := l.waiters.wait(&l.mu, w)
	if !canceled {
		return r.err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.err == nil {
		// Admitted just as we were canceling: give it back, so a
		// canceled Wait holds nothing.
		l.release(d)
	}
	// w may have been the head; its successor may fit now.
	l.admitLocked()
	return context.Cause(ctx)
}

// TryWait admits d only when no one is waiting and Claim takes it. It
// returns false when d cannot be admitted or after [Gate.Close].
func (l *Gate[D]) TryWait(d D) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.waiters.front(); l.closed || ok {
		return false
	}
	return l.claim(d)
}

// Put gives back d's capacity through Release, then admits waiters from
// the front of the line while Claim takes them. Put may admit several
// waiters and works after Close.
func (l *Gate[D]) Put(d D) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.release(d)
	l.admitLocked()
}

// Close wakes queued callers. Later [Gate.Wait] calls return [ErrClosed], and
// [Gate.TryWait] returns false. Close is idempotent.
func (l *Gate[D]) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	l.waiters.close()
}

// admitLocked admits waiters from the head of the line while Claim takes
// them. No demand is offered to Claim while another is ahead of it in line.
func (l *Gate[D]) admitLocked() {
	for {
		w, ok := l.waiters.front()
		if !ok || !l.claim(w.d) {
			return
		}
		l.waiters.pop(struct{}{}, nil)
	}
}

func (l *Gate[D]) claim(d D) bool {
	return l.Claim == nil || l.Claim(d)
}

func (l *Gate[D]) release(d D) {
	if l.Release != nil {
		l.Release(d)
	}
}
