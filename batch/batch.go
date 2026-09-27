// Package batch fills fixed-size batches from concurrent callers in arrival
// order, for work that is cheaper done many elements at a time, like GPU
// inference.
//
// Callers bring their input to the open batch with [Batcher.Reserve], say
// how much output they want, and read it through the [Handle] it returns.
// The batch's owner ranges over [Batcher.Batches] and runs each [Batch] it
// yields. A Batcher reuses a fixed set of buffers, allocated once by [New],
// so a batch's memory is its callers' only until they call [Handle.Done].
package batch

import (
	"context"
	"errors"
	"iter"
	"sync"
	"time"

	"blake.io/wait"
)

// A Batch is one batch's memory, as Batches yields it: the callers' input,
// in arrival order, and room for their output. Reservation i has input
// In[InOffsets[i]:InOffsets[i+1]] and output
// Out[OutOffsets[i]:OutOffsets[i+1]]. The owner reads In and writes Out,
// which starts zeroed.
type Batch[In, Out any] struct {
	In         []In
	Out        []Out
	InOffsets  []int // one more than the reservations; InOffsets[0] is 0
	OutOffsets []int // one more than the reservations; OutOffsets[0] is 0
}

// A buffer holds one batch at a time. The holder of the Batcher's turn
// grows in, out, and the offsets; the rest of the buffer's bookkeeping is
// guarded by mu. Once its batch has run and every Handle of it is done,
// the buffer is cleared, gen is bumped so that stale Handles no longer
// match, and it goes back to the Batcher's free list.
type buffer[In, Out any] struct {
	b      *Batcher[In, Out]
	inArr  []In  // the whole input buffer; never replaced
	outArr []Out // the whole output buffer; never replaced
	in     []In  // inArr[:reserved]
	out    []Out // outArr[:reserved]
	inOff  []int // inOff[:slots+1] are the input offsets
	outOff []int // outOff[:slots+1] are the output offsets

	mu       sync.Mutex // guards the fields below
	gen      uint64
	live     []bool // live[slot]: that reservation's Handle is not done
	slots    int    // reservations made in this batch
	n        int    // Handles not done
	finished bool   // ran, or failed with err
	err      error

	// ran wakes Waits once the batch finishes: finish adds a token for
	// each Handle not done, and a Wait takes one and keeps it. Tokens no
	// Wait took are drained when the buffer is recycled.
	ran wait.List[struct{}]
}

// A Batcher fills batches with input of type In and output of type Out,
// and hands each to its owner to run. It reuses a fixed number of
// buffers: one batch fills while the others run or wait for their callers
// to call Done. With every buffer in use, the owner, and so every caller,
// waits for a free one.
//
// A Batcher must be made with New. It is safe for concurrent use.
type Batcher[In, Out any] struct {
	// MaxDelay is how long a batch waits, after its first reservation,
	// for more callers before Batches yields it. A batch that is full,
	// or that a caller has stepped aside from, is yielded at once. Zero
	// means a batch is yielded as soon as anything is reserved. MaxDelay
	// must not change once the Batcher is in use.
	MaxDelay time.Duration

	inSize, outSize int

	// This is package wait's batch example with a token in each List in
	// place of the batch. Holding turn's token is your turn to reserve in
	// open. A caller that does not fit takes a place in next before giving
	// back the turn. The owner holds next's token between batches, so those
	// places wait for the next batch.
	turn, next wait.List[struct{}]
	mine       wait.Ticket[struct{}] // next's token; used by the turn's holder
	free       wait.List[*buffer[In, Out]]

	kick    chan struct{} // wakes an owner waiting for a batch to be ready
	closedc chan struct{} // closed by Close

	mu     sync.Mutex // guards the fields below; held to replace open
	closed bool
	open   *buffer[In, Out]
	nres   int       // reservations in open
	first  time.Time // when open's first reservation was made
	full   bool      // open is full, or a caller stepped aside from it
}

// New returns a Batcher of batches of up to inSize input elements and
// outSize output elements, using buffers buffers of that size, allocated
// now. It takes at least two buffers, one to fill while another runs; an
// owner running batches from several goroutines wants one more buffer than
// it has goroutines. If inSize is negative, outSize is not positive, or
// buffers is less than two, New panics.
func New[In, Out any](inSize, outSize, buffers int) *Batcher[In, Out] {
	if inSize < 0 || outSize < 1 {
		panic("batch: bad batch size")
	}
	if buffers < 2 {
		panic("batch: fewer than two buffers")
	}
	b := &Batcher[In, Out]{
		inSize:  inSize,
		outSize: outSize,
		kick:    make(chan struct{}, 1),
		closedc: make(chan struct{}),
	}
	for i := range buffers {
		// Every reservation takes at least one output element, so a
		// batch holds at most outSize of them.
		u := &buffer[In, Out]{
			b:      b,
			inArr:  make([]In, inSize),
			outArr: make([]Out, outSize),
			inOff:  make([]int, outSize+1),
			outOff: make([]int, outSize+1),
			live:   make([]bool, outSize),
		}
		u.in, u.out = u.inArr[:0], u.outArr[:0]
		if i == 0 {
			b.open = u
		} else {
			b.free.Add(u)
		}
	}
	b.turn.Add(struct{}{})
	b.next.Add(struct{}{})
	b.mine = b.next.Take(context.Background())
	return b
}

// ErrRange is returned by [Batcher.Reserve] when a reservation cannot fit
// in a batch, or asks for no output.
var ErrRange = errors.New("batch: reservation out of range")

// Reserve waits its turn, copies in into the open batch, and reserves n
// elements of the batch's output, returning a Handle to them. Reserve does
// not keep in. Callers are served in arrival order; one that does not fit
// steps aside, so that callers behind it may fill the batch, and goes first
// in the next one, in the order callers stepped aside.
//
// Call Done on the Handle, with defer, before reserving again: with every
// buffer in use, the Batcher waits for that Done before it can open the
// batch a new reservation needs.
//
// If in is longer than a batch's input, or n is not positive or larger
// than a batch's output, Reserve returns ErrRange. Reserve returns
// [wait.ErrClosed] after Close, and ctx's cause, reserving nothing, if ctx
// is done first.
func (b *Batcher[In, Out]) Reserve(ctx context.Context, in []In, n int) (Handle[In, Out], error) {
	if len(in) > b.inSize || n < 1 || n > b.outSize {
		return Handle[In, Out]{}, ErrRange
	}
	t := b.turn.Take(ctx)
	_, err := t.Value()
	for err == nil && !b.open.fits(len(in), n) {
		b.mu.Lock()
		b.full = true
		b.mu.Unlock()
		b.wake()
		aside := b.next.Take(ctx) // keep a place before giving back the turn
		t.Done()
		t = aside
		_, err = t.Value()
	}
	defer t.Done()
	if err != nil {
		return Handle[In, Out]{}, err
	}
	h, dst, err := b.reserve(len(in), n)
	if err == nil {
		// Still holding the token, so the owner can't take the batch
		// before the copy is done.
		copy(dst, in)
	}
	return h, err
}

// fits reports whether u has room for nin input and nout output elements.
func (u *buffer[In, Out]) fits(nin, nout int) bool {
	return nin <= cap(u.in)-len(u.in) && nout <= cap(u.out)-len(u.out)
}

// reserve reserves nin input and nout output elements of the open batch,
// which fit, and returns the Handle and the input elements to fill. The
// caller holds the turn, or next's token while the owner hands it through.
func (b *Batcher[In, Out]) reserve(nin, nout int) (Handle[In, Out], []In, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return Handle[In, Out]{}, nil, wait.ErrClosed // a closed List still hands out its ready token
	}
	u := b.open
	u.mu.Lock()
	il, ol := len(u.in), len(u.out)
	u.in, u.out = u.in[:il+nin], u.out[:ol+nout]
	slot := u.slots
	u.slots++
	u.inOff[slot+1], u.outOff[slot+1] = il+nin, ol+nout
	u.live[slot] = true
	u.n++
	h := Handle[In, Out]{u, u.gen, slot, u.outArr[ol : ol+nout : ol+nout]}
	u.mu.Unlock()

	if b.nres == 0 {
		b.first = time.Now()
		b.wake()
	}
	b.nres++
	if len(u.out) == cap(u.out) || len(u.in) == cap(u.in) && cap(u.in) > 0 {
		b.full = true
		b.wake()
	}
	return h, u.inArr[il : il+nin : il+nin], nil
}

// wake wakes an owner waiting in Batches, or leaves a token for the next.
func (b *Batcher[In, Out]) wake() {
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

// Batches returns an iterator over the Batcher's batches, in order. Each
// iteration waits until a batch is ready, then yields it. A batch is ready
// when its input or output is full, when a caller has stepped aside from
// it, or MaxDelay after its first reservation. Callers fill the next batch
// while the loop body runs, and when the body returns, even by break or
// panic, the batch's Handles get their results.
//
// The iteration stops when ctx is done or the Batcher is closed. Several
// goroutines may range over Batches at once; each batch goes to one.
func (b *Batcher[In, Out]) Batches(ctx context.Context) iter.Seq[Batch[In, Out]] {
	return func(yield func(Batch[In, Out]) bool) {
		timer := time.NewTimer(time.Hour)
		timer.Stop()
		defer timer.Stop()
		for {
			if b.await(ctx, timer) != nil {
				return
			}
			u, err := b.cut(ctx)
			if err != nil {
				return
			}
			if u != nil && !b.run(u, yield) {
				return
			}
		}
	}
}

// readyLocked reports whether the open batch is ready to run, or else how
// long until it is, or 0 if nothing is reserved. b.mu is held.
func (b *Batcher[In, Out]) readyLocked() (bool, time.Duration) {
	if b.nres == 0 {
		return false, 0
	}
	d := b.MaxDelay - time.Since(b.first)
	return b.full || d <= 0, d
}

// await waits until the open batch is ready to run.
func (b *Batcher[In, Out]) await(ctx context.Context, timer *time.Timer) error {
	for {
		b.mu.Lock()
		closed := b.closed
		ok, d := b.readyLocked()
		b.mu.Unlock()
		if closed {
			return wait.ErrClosed
		}
		if ok {
			return nil
		}
		var fire <-chan time.Time
		if d > 0 {
			timer.Reset(d)
			fire = timer.C
		}
		select {
		case <-b.kick:
		case <-fire:
		case <-b.closedc:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

// cut waits for the turn and, if the open batch is still ready, for a free
// buffer. It opens the next batch in the free buffer and returns the
// buffer of the batch it replaced. It returns nil if the batch is no
// longer ready because another owner took it first.
func (b *Batcher[In, Out]) cut(ctx context.Context) (*buffer[In, Out], error) {
	t := b.turn.Take(ctx)
	defer t.Done()
	if _, err := t.Value(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	closed := b.closed
	ok, _ := b.readyLocked()
	b.mu.Unlock()
	if closed {
		return nil, wait.ErrClosed
	}
	if !ok {
		return nil, nil
	}

	// Holding the turn, wait for a buffer: with every buffer in use, every
	// caller waits here too.
	ft := b.free.Take(ctx)
	nu, err := ft.Value()
	ft.Leave()
	ft.Done()
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, wait.ErrClosed
	}
	u := b.open
	b.open = nu
	b.nres, b.full, b.first = 0, false, time.Time{}
	b.mu.Unlock()

	// Pass next's token through everyone who stepped aside, then take it
	// back behind them. The wait has no ctx: each holds the token only
	// long enough to reserve, and Close ends it. Its only error is
	// ErrClosed, which the next call reports.
	mine := b.next.Take(context.Background())
	b.mine.Done()
	mine.Value()
	b.mine = mine
	return u, nil
}

// run yields u's batch and then gives its Handles their results.
func (b *Batcher[In, Out]) run(u *buffer[In, Out], yield func(Batch[In, Out]) bool) bool {
	defer u.finish(nil)
	return yield(Batch[In, Out]{
		In:         u.in[:len(u.in):len(u.in)],
		Out:        u.out[:len(u.out):len(u.out)],
		InOffsets:  u.inOff[: u.slots+1 : u.slots+1],
		OutOffsets: u.outOff[: u.slots+1 : u.slots+1],
	})
}

// finish ends u's batch with err, and frees u if no Handle of it is left.
func (u *buffer[In, Out]) finish(err error) {
	u.mu.Lock()
	u.err = err
	u.finished = true
	// Add the tokens before unlocking, so that a Done that frees u can't
	// drain ran before they are in.
	for range u.n {
		u.ran.Add(struct{}{})
	}
	free := u.n == 0
	if free {
		u.gen++
	}
	u.mu.Unlock()
	if free {
		u.recycle()
	}
}

// recycle clears what u's last batch used and returns u to the free list.
// No Handle of that batch is left, and gen no longer matches stale ones,
// so nothing else touches u until the free list hands it out again. After
// Close, u is dropped instead: the open batch may still be read by a
// Reserve that took the turn before Close.
func (u *buffer[In, Out]) recycle() {
	b := u.b
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return
	}
	clear(u.in)
	clear(u.out)
	clear(u.live[:u.slots])
	u.in, u.out = u.inArr[:0], u.outArr[:0]
	u.slots, u.finished, u.err = 0, false, nil
	for {
		t, ok := u.ran.TryTake()
		if !ok {
			break
		}
		t.Leave()
		t.Done()
	}
	b.free.Add(u)
}

// Close fails every caller waiting to reserve, and the Handles of the open
// batch, with [wait.ErrClosed], and makes later calls of Reserve return
// it; it ends every Batches iteration once its loop body returns. A batch
// already yielded still delivers to its Handles. Close is idempotent.
func (b *Batcher[In, Out]) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	close(b.closedc)
	u := b.open
	b.mu.Unlock()
	u.finish(wait.ErrClosed)
	b.turn.Close()
	b.next.Close()
	b.free.Close()
}

// A Handle is a caller's reservation in a batch. The zero Handle
// reserves nothing.
//
// The output Wait returns is the caller's until Done, and only until then:
// after Done, the Batcher clears the memory and reuses it for a later
// batch. Copy anything needed longer. Call Done promptly, with defer:
// callers share a few buffers, so one slow to call Done keeps the owner,
// and every caller behind it, waiting for a free buffer.
//
// A Handle is for use by one goroutine at a time.
type Handle[In, Out any] struct {
	u    *buffer[In, Out]
	gen  uint64
	slot int
	out  []Out
}

// Wait waits for the Handle's batch to run and returns the Handle's
// output, capped so that appending cannot reach a neighbor's. Wait returns
// [wait.ErrClosed] if the Batcher is closed before the batch is yielded,
// ctx's cause if ctx is done first, and [wait.ErrDone] after Done. A run
// that races with ctx wins. On the zero Handle, Wait returns nil, nil.
func (h Handle[In, Out]) Wait(ctx context.Context) ([]Out, error) {
	u := h.u
	if u == nil {
		return nil, nil
	}
	u.mu.Lock()
	if u.gen != h.gen || !u.live[h.slot] {
		u.mu.Unlock()
		return nil, wait.ErrDone
	}
	finished := u.finished
	u.mu.Unlock()
	if !finished {
		t := u.ran.Take(ctx)
		_, err := t.Value()
		t.Leave()
		t.Done()
		if err != nil {
			u.mu.Lock()
			finished = u.finished // a run that races with ctx wins
			u.mu.Unlock()
			if !finished {
				return nil, err
			}
		}
	}
	// u stays this batch's until h is done, so err is still ours.
	if u.err != nil {
		return nil, u.err
	}
	return h.out, nil
}

// Done gives the Handle's output back. When every Handle of a batch is
// done and the batch has run, its buffer is cleared and free for a later
// batch. Done does nothing after the first call, on the Handle or any
// copy of it.
func (h Handle[In, Out]) Done() {
	u := h.u
	if u == nil {
		return
	}
	u.mu.Lock()
	if u.gen != h.gen || !u.live[h.slot] {
		u.mu.Unlock()
		return
	}
	u.live[h.slot] = false
	u.n--
	free := u.n == 0 && u.finished
	if free {
		u.gen++
	}
	u.mu.Unlock()
	if free {
		u.recycle()
	}
}
