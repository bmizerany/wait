package wait

import (
	"context"
	"sync"
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

	mu      sync.Mutex // guards waiters and closed
	waiters line[D, struct{}]
	closed  bool
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
	if _, ok := l.waiters.front(); !ok && l.fill(d) {
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
		// Admitted just as we were canceling: refund, so a canceled
		// Wait holds nothing.
		l.refill(d)
	}
	// w may have been the head; its successor may fit now.
	l.admitLocked()
	return context.Cause(ctx)
}

// TryWait admits d only when no one is waiting and Fill accepts it. It
// returns false when d cannot be admitted or after [Gate.Close].
func (l *Gate[D]) TryWait(d D) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.waiters.front(); l.closed || ok {
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
	l.closed = true
	l.waiters.close()
}

// admitLocked admits waiters from the head of the line while Fill accepts
// them. No demand is offered to Fill while another is ahead of it in line.
func (l *Gate[D]) admitLocked() {
	for {
		w, ok := l.waiters.front()
		if !ok || !l.fill(w.d) {
			return
		}
		l.waiters.pop(struct{}{}, nil)
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
