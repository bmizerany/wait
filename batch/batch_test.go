package batch_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"blake.io/wait"
	"blake.io/wait/batch"
)

// in returns an input of n elements for Reserve.
func in(n int) []float32 { return make([]float32, n) }

// A fill writes 100*k+i to element i of batch k, so a caller's results say
// which batch it landed in and where.
func fill(k int, mem []float32) {
	for i := range mem {
		mem[i] = float32(100*k + i)
	}
}

// reserve starts a caller for n elements and waits until it has reserved
// or stepped aside, so callers arrive in the order reserve is called. The
// Handle arrives on the returned channel.
func reserve(t *testing.T, b *batch.Batcher[float32, float32], n int) <-chan batch.Handle[float32, float32] {
	c := make(chan batch.Handle[float32, float32], 1)
	go func() {
		h, err := b.Reserve(t.Context(), in(n), n)
		if err != nil {
			t.Errorf("Reserve(%d) = %v, want nil", n, err)
		}
		c <- h
	}()
	synctest.Wait()
	return c
}

// wantHandles checks that the callers started by reserve got results want.
func wantHandles(t *testing.T, c []<-chan batch.Handle[float32, float32], want [][]float32) {
	t.Helper()
	for i, want := range want {
		mem, err := (<-c[i]).Wait(t.Context())
		if err != nil || !slices.Equal(mem, want) {
			t.Errorf("caller %d: Wait() = %v, %v, want %v, nil", i, mem, err, want)
		}
	}
}

// An owner ranges over a Batcher's batches in its own goroutine. For batch
// k, it sends the batch's size and whether it was all zeros on yielded,
// fills it with fill(k), and returns from the loop body when step is sent
// to or closed.
type owner struct {
	yielded chan batchInfo
	step    chan struct{}
	done    chan struct{}
}

type batchInfo struct {
	size  int
	clean bool
}

func startOwner[In any](ctx context.Context, b *batch.Batcher[In, float32]) *owner {
	return startOwners(ctx, b, 1)
}

// startOwners is startOwner with n goroutines ranging over Batches, all
// sending on the same channels. Batch numbers count per goroutine.
func startOwners[In any](ctx context.Context, b *batch.Batcher[In, float32], n int) *owner {
	o := &owner{yielded: make(chan batchInfo), step: make(chan struct{}), done: make(chan struct{})}
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			k := 0
			for bt := range b.Batches(ctx) {
				k++
				clean := !slices.ContainsFunc(bt.Out, func(f float32) bool { return f != 0 })
				o.yielded <- batchInfo{len(bt.Out), clean}
				fill(k, bt.Out)
				<-o.step
			}
		})
	}
	go func() {
		wg.Wait()
		close(o.done)
	}()
	synctest.Wait()
	return o
}

// run lets the owner yield its next batch and returns from its loop body
// once the test has seen it. It fails t if no batch is ready.
func (o *owner) run(t *testing.T) batchInfo {
	t.Helper()
	synctest.Wait()
	select {
	case bi := <-o.yielded:
		o.step <- struct{}{}
		synctest.Wait()
		return bi
	default:
		t.Fatal("no batch yielded, want one")
		return batchInfo{}
	}
}

// idle fails t if the owner has yielded a batch.
func (o *owner) idle(t *testing.T) {
	t.Helper()
	synctest.Wait()
	select {
	case bi := <-o.yielded:
		t.Fatalf("batch of %d yielded, want none yet", bi.size)
	default:
	}
}

func TestBatches(t *testing.T) { synctest.Test(t, testBatches) }
func testBatches(t *testing.T) {
	b := batch.New[float32, float32](16, 16, 4)
	var hs [7]<-chan batch.Handle[float32, float32]
	for i, n := range []int{6, 8, 12, 2, 5, 3} {
		hs[i] = reserve(t, b, n)
	}
	o := startOwner(t.Context(), b)

	// 12 stepped aside from batch 1, so batch 1 runs at once. While it
	// runs, batch 2 holds 12 and 3, 5 stepped aside, and 4 arrives and
	// lines up behind 5.
	bi := <-o.yielded
	hs[6] = reserve(t, b, 4)
	o.step <- struct{}{}
	var sizes []int
	sizes = append(sizes, bi.size, o.run(t).size, o.run(t).size)
	if want := []int{16, 15, 9}; !slices.Equal(sizes, want) {
		t.Errorf("batch sizes = %v, want %v", sizes, want)
	}
	wantHandles(t, hs[:], [][]float32{
		{100, 101, 102, 103, 104, 105},
		{106, 107, 108, 109, 110, 111, 112, 113},
		{200, 201, 202, 203, 204, 205, 206, 207, 208, 209, 210, 211},
		{114, 115},
		{300, 301, 302, 303, 304},
		{212, 213, 214},
		{305, 306, 307, 308},
	})
	b.Close()
	<-o.done
}

func TestAsideTwice(t *testing.T) { synctest.Test(t, testAsideTwice) }
func testAsideTwice(t *testing.T) {
	b := batch.New[float32, float32](16, 16, 4)
	var hs [4]<-chan batch.Handle[float32, float32]
	hs[0] = reserve(t, b, 8)
	hs[1] = reserve(t, b, 12) // steps aside
	hs[2] = reserve(t, b, 9)  // steps aside, behind 12
	o := startOwner(t.Context(), b)
	bi := <-o.yielded
	// Batch 2 holds 12; 9 stepped aside again. The 5 arriving now fits
	// neither and lines up behind 9.
	hs[3] = reserve(t, b, 5)
	o.step <- struct{}{}
	if sizes := []int{bi.size, o.run(t).size, o.run(t).size}; !slices.Equal(sizes, []int{8, 12, 14}) {
		t.Errorf("batch sizes = %v, want [8 12 14]", sizes)
	}
	wantHandles(t, hs[:], [][]float32{
		{100, 101, 102, 103, 104, 105, 106, 107},
		{200, 201, 202, 203, 204, 205, 206, 207, 208, 209, 210, 211},
		{300, 301, 302, 303, 304, 305, 306, 307, 308},
		{309, 310, 311, 312, 313},
	})
	b.Close()
	<-o.done
}

func TestSwap(t *testing.T) { synctest.Test(t, testSwap) }
func testSwap(t *testing.T) {
	// With two buffers, one batch fills while the other is run and read.
	// A full batch waits until the other buffer's callers are done with
	// it, and so does every caller; then the two trade places.
	b := batch.New[float32, float32](4, 4, 2)
	o := startOwner(t.Context(), b)
	h1 := <-reserve(t, b, 4)
	o.run(t)
	h2c := reserve(t, b, 4) // fills the other buffer
	c := reserve(t, b, 1)
	o.idle(t)
	select {
	case <-c:
		t.Fatal("Reserve(1) returned while every buffer is in use")
	default:
	}
	if mem, err := h1.Wait(t.Context()); err != nil || mem[0] != 100 {
		t.Fatalf("h1.Wait() = %v, %v, want batch 1's elements", mem, err)
	}

	h1.Done() // frees batch 1's buffer: batch 2 runs, batch 3 opens in it
	if bi := o.run(t); bi.size != 4 {
		t.Errorf("batch 2 = %+v, want 4 elements", bi)
	}
	o.idle(t) // batch 3 holds 1, and batch 2's buffer is in use
	(<-h2c).Done()
	if bi := o.run(t); bi.size != 1 || !bi.clean {
		t.Errorf("batch 3 = %+v, want 1 element, cleared", bi)
	}
	(<-c).Done()
	b.Close()
	<-o.done
}

func TestDone(t *testing.T) { synctest.Test(t, testDone) }
func testDone(t *testing.T) {
	b := batch.New[float32, float32](4, 4, 2)
	b.MaxDelay = time.Hour // only full batches run
	o := startOwner(t.Context(), b)
	h1 := <-reserve(t, b, 2)
	h2 := <-reserve(t, b, 2)
	o.run(t)

	// Done twice, and through a copy, gives back h1's slots once: the
	// buffer is not free while h2 holds its own.
	h1.Done()
	h1.Done()
	cp := h1
	cp.Done()
	if _, err := h1.Wait(t.Context()); !errors.Is(err, wait.ErrDone) {
		t.Errorf("Wait() after Done = %v, want ErrDone", err)
	}
	h3c := reserve(t, b, 4)
	o.idle(t)
	h2.Done() // frees batch 1's buffer
	o.run(t)

	// Batch 3 reuses batch 1's buffer. h1 is stale now: its Done must not
	// count against batch 3.
	c := reserve(t, b, 4)
	(<-h3c).Done()
	o.run(t)
	h4 := <-c
	h1.Done()
	d := reserve(t, b, 4)
	o.idle(t) // h4 still holds its buffer
	if mem, err := h4.Wait(t.Context()); err != nil || len(mem) != 4 {
		t.Errorf("h4.Wait() = %v, %v, want 4 elements", mem, err)
	}
	if _, err := h1.Wait(t.Context()); !errors.Is(err, wait.ErrDone) {
		t.Errorf("stale Wait() = %v, want ErrDone", err)
	}
	h4.Done()
	o.run(t)
	(<-d).Done()
	b.Close()
	<-o.done
}

func TestMaxDelay(t *testing.T) { synctest.Test(t, testMaxDelay) }
func testMaxDelay(t *testing.T) {
	const d = 10 * time.Millisecond
	b := batch.New[float32, float32](4, 4, 2)
	b.MaxDelay = d
	o := startOwner(t.Context(), b)

	// The delay counts from the batch's first reservation, not its last.
	h := <-reserve(t, b, 1)
	time.Sleep(d / 2)
	h2 := <-reserve(t, b, 1)
	time.Sleep(d/2 - time.Nanosecond)
	o.idle(t)
	time.Sleep(time.Nanosecond)
	if bi := o.run(t); bi.size != 2 {
		t.Errorf("batch after MaxDelay = %d elements, want 2", bi.size)
	}
	h.Done()
	h2.Done()

	// A full batch, or one a caller stepped aside from, doesn't wait.
	h = <-reserve(t, b, 4)
	if bi := o.run(t); bi.size != 4 {
		t.Errorf("full batch = %d elements, want 4", bi.size)
	}
	h.Done()
	h = <-reserve(t, b, 3)
	c := reserve(t, b, 2) // steps aside
	if bi := o.run(t); bi.size != 3 {
		t.Errorf("batch stepped aside from = %d elements, want 3", bi.size)
	}
	h.Done()
	time.Sleep(d)
	o.run(t)
	(<-c).Done()
	b.Close()
	<-o.done
}

func TestFull(t *testing.T) { synctest.Test(t, testFull) }
func testFull(t *testing.T) {
	// A batch is full when its input or its output is, and a caller that
	// doesn't fit in either steps aside. A batch with no input to fill
	// waits for its output to.
	b := batch.New[uint32, float32](4, 8, 3)
	b.MaxDelay = time.Hour
	o := startOwner(t.Context(), b)
	h1, err := b.Reserve(t.Context(), make([]uint32, 4), 1)
	if err != nil {
		t.Fatal(err)
	}
	if n := o.run(t).size; n != 1 {
		t.Errorf("batch with its input full = %d outputs, want 1", n)
	}
	h2, err := b.Reserve(t.Context(), make([]uint32, 3), 1)
	if err != nil {
		t.Fatal(err)
	}
	c := make(chan batch.Handle[uint32, float32], 1)
	go func() {
		h, err := b.Reserve(t.Context(), make([]uint32, 2), 1) // output fits, input doesn't
		if err != nil {
			t.Error(err)
		}
		c <- h
	}()
	if n := o.run(t).size; n != 1 {
		t.Errorf("batch stepped aside from = %d outputs, want 1", n)
	}
	h1.Done()
	h2.Done()
	(<-c).Done()
	b.Close()
	<-o.done

	b = batch.New[uint32, float32](0, 4, 2)
	b.MaxDelay = time.Hour
	o = startOwner(t.Context(), b)
	h1, err = b.Reserve(t.Context(), nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	o.idle(t)
	h2, err = b.Reserve(t.Context(), nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if n := o.run(t).size; n != 4 {
		t.Errorf("batch with its output full = %d outputs, want 4", n)
	}
	h1.Done()
	h2.Done()
	b.Close()
	<-o.done
}

func TestOwners(t *testing.T) { synctest.Test(t, testOwners) }
func testOwners(t *testing.T) {
	// Several owners share the batches: each batch goes to one of them,
	// and every caller gets its own elements of one batch.
	b := batch.New[float32, float32](8, 8, 4)
	var mu sync.Mutex
	next := 0
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			for bt := range b.Batches(t.Context()) {
				mu.Lock()
				next++
				k := next
				mu.Unlock()
				fill(k, bt.Out)
			}
		})
	}
	var callers sync.WaitGroup
	for i := range 60 {
		callers.Go(func() {
			n := 1 + i%5
			h, err := b.Reserve(t.Context(), in(n), n)
			if err != nil {
				t.Errorf("Reserve(%d) = %v", n, err)
				return
			}
			defer h.Done()
			mem, err := h.Wait(t.Context())
			if err != nil || len(mem) != n {
				t.Errorf("Wait() = %v, %v, want %d elements", mem, err, n)
				return
			}
			k := int(mem[0]) / 100
			for j, f := range mem {
				if int(f)/100 != k || j > 0 && f != mem[j-1]+1 {
					t.Errorf("Wait() = %v, want consecutive elements of one batch", mem)
					return
				}
			}
		})
	}
	callers.Wait()
	b.Close()
	wg.Wait()
}

func TestOwnersWait(t *testing.T) { synctest.Test(t, testOwnersWait) }
func testOwnersWait(t *testing.T) {
	// Two owners see batch 2 ready. One takes it; the other, next in line
	// for the turn, must find batch 3 not yet ready and wait MaxDelay for
	// it, not take it at once.
	b := batch.New[float32, float32](4, 4, 2)
	b.MaxDelay = time.Hour
	o := startOwners(t.Context(), b, 2)
	h1 := <-reserve(t, b, 4)
	<-o.yielded // one owner runs batch 1 and holds it
	h2 := <-reserve(t, b, 3)
	h3c := reserve(t, b, 2) // steps aside: batch 2 is ready, and the other owner waits for a buffer
	o.step <- struct{}{}    // the first owner comes back, finds batch 2 ready, and waits for the turn
	synctest.Wait()
	h1.Done() // batch 2 runs; batch 3 opens holding 2
	if bi := o.run(t); bi.size != 3 {
		t.Fatalf("batch 2 = %+v, want 3 elements", bi)
	}
	h2.Done()
	o.idle(t) // batch 3 waits for MaxDelay, though an owner is free
	time.Sleep(time.Hour)
	if bi := o.run(t); bi.size != 2 {
		t.Errorf("batch 3 = %+v, want 2 elements", bi)
	}
	(<-h3c).Done()
	b.Close()
	<-o.done
}

var errStop = errors.New("stop")

func TestCancel(t *testing.T) { synctest.Test(t, testCancel) }
func testCancel(t *testing.T) {
	b := batch.New[float32, float32](4, 4, 2)
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errStop)
	if _, err := b.Reserve(ctx, in(4), 4); err != errStop {
		t.Errorf("Reserve(4) with a done ctx = %v, want errStop", err)
	}

	// A ready batch isn't yielded to a done ctx.
	h := <-reserve(t, b, 4)
	for range b.Batches(ctx) {
		t.Error("Batches yielded a batch to a done ctx")
	}
	o := startOwner(t.Context(), b)
	o.run(t)
	if mem, err := h.Wait(t.Context()); err != nil || len(mem) != 4 {
		t.Errorf("Wait() = %v, %v, want 4 elements", mem, err)
	}
	h.Done()
	b.Close()
	<-o.done
}

func TestCancelAside(t *testing.T) { synctest.Test(t, testCancelAside) }
func testCancelAside(t *testing.T) {
	b := batch.New[float32, float32](16, 16, 4)
	var hs [3]<-chan batch.Handle[float32, float32]
	hs[0] = reserve(t, b, 8)
	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() {
		_, err := b.Reserve(ctx, in(12), 12) // steps aside
		errc <- err
	}()
	synctest.Wait()
	hs[1] = reserve(t, b, 9) // steps aside, behind 12
	cancel()
	synctest.Wait()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Errorf("Reserve(12) after cancel = %v, want context.Canceled", err)
	}
	o := startOwner(t.Context(), b)
	bi := <-o.yielded
	hs[2] = reserve(t, b, 7) // behind 9 in batch 2
	o.step <- struct{}{}
	if sizes := []int{bi.size, o.run(t).size}; !slices.Equal(sizes, []int{8, 16}) {
		t.Errorf("batch sizes = %v, want [8 16]", sizes)
	}
	wantHandles(t, hs[:], [][]float32{
		{100, 101, 102, 103, 104, 105, 106, 107},
		{200, 201, 202, 203, 204, 205, 206, 207, 208},
		{209, 210, 211, 212, 213, 214, 215},
	})
	b.Close()
	<-o.done
}

func TestCancelWait(t *testing.T) { synctest.Test(t, testCancelWait) }
func testCancelWait(t *testing.T) {
	b := batch.New[float32, float32](16, 16, 2)
	h, err := b.Reserve(t.Context(), in(4), 4)
	if err != nil {
		t.Fatalf("Reserve(4) = %v, want nil", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if mem, err := h.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait() before the run = %v, %v, want nil, context.Canceled", mem, err)
	}
	o := startOwner(t.Context(), b)
	o.run(t)
	// The canceled Wait did not give up the reservation, and a run beats
	// a done ctx.
	mem, err := h.Wait(ctx)
	if want := []float32{100, 101, 102, 103}; err != nil || !slices.Equal(mem, want) {
		t.Errorf("Wait() after the run = %v, %v, want %v, nil", mem, err, want)
	}
	h.Done()
	b.Close()
	<-o.done
}

func TestBreak(t *testing.T) { synctest.Test(t, testBreak) }
func testBreak(t *testing.T) {
	// A loop body that breaks, or panics, still delivers its batch.
	b := batch.New[float32, float32](4, 4, 2)
	h := <-reserve(t, b, 4)
	for bt := range b.Batches(t.Context()) {
		fill(1, bt.Out)
		break
	}
	if mem, err := h.Wait(t.Context()); err != nil || mem[0] != 100 {
		t.Errorf("Wait() after break = %v, %v, want batch 1's elements", mem, err)
	}
	h.Done()

	h = <-reserve(t, b, 4)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("loop body did not panic")
			}
		}()
		for bt := range b.Batches(t.Context()) {
			fill(2, bt.Out)
			panic("gpu")
		}
	}()
	if mem, err := h.Wait(t.Context()); err != nil || mem[0] != 200 {
		t.Errorf("Wait() after panic = %v, %v, want batch 2's elements", mem, err)
	}
	h.Done()
}

func TestHoldAndReserve(t *testing.T) { synctest.Test(t, testHoldAndReserve) }
func testHoldAndReserve(t *testing.T) {
	// A caller that reserves again while holding a Handle of a batch that
	// has run can wait on itself: with both buffers in use, the owner
	// waits for that Handle's Done before it can open the batch the new
	// reservation needs.
	b := batch.New[float32, float32](4, 4, 2)
	o := startOwner(t.Context(), b)
	h1 := <-reserve(t, b, 4)
	o.run(t)
	h2 := <-reserve(t, b, 4) // fills the other buffer; the owner now waits
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := b.Reserve(ctx, in(1), 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Reserve(1) while holding h1 = %v, want to wait until its ctx is done", err)
	}
	h1.Done()
	o.run(t)
	h2.Done()
	b.Close()
	<-o.done
}

func TestClose(t *testing.T) { synctest.Test(t, testClose) }
func testClose(t *testing.T) {
	b := batch.New[float32, float32](16, 16, 4)
	h8 := reserve(t, b, 8)
	h12 := reserve(t, b, 12) // steps aside
	o := startOwner(t.Context(), b)
	<-o.yielded // batch 1 is running

	// While it runs, 12 is in batch 2, 4 fits beside it, and 8 steps aside.
	h4 := <-reserve(t, b, 4)
	errc := make(chan error, 1)
	go func() {
		_, err := b.Reserve(t.Context(), in(8), 8)
		errc <- err
	}()
	synctest.Wait()

	b.Close()
	b.Close()
	if err := <-errc; !errors.Is(err, wait.ErrClosed) {
		t.Errorf("Reserve(8) after Close = %v, want ErrClosed", err)
	}
	for _, h := range []batch.Handle[float32, float32]{<-h12, h4} {
		if mem, err := h.Wait(t.Context()); !errors.Is(err, wait.ErrClosed) {
			t.Errorf("Wait() on batch 2 after Close = %v, %v, want nil, ErrClosed", mem, err)
		}
	}
	if _, err := b.Reserve(t.Context(), in(1), 1); !errors.Is(err, wait.ErrClosed) {
		t.Errorf("Reserve(1) after Close = %v, want ErrClosed", err)
	}

	// The running batch still delivers, and then the loop ends.
	o.step <- struct{}{}
	<-o.done
	wantHandles(t, []<-chan batch.Handle[float32, float32]{h8}, [][]float32{{100, 101, 102, 103, 104, 105, 106, 107}})
}

func TestReserveRange(t *testing.T) {
	b := batch.New[uint32, float32](8, 4, 2)
	for _, tt := range []struct{ nin, nout int }{
		{9, 1}, // more input than a batch holds
		{1, 5}, // more output than a batch holds
		{1, 0}, // no output
		{1, -1},
	} {
		if _, err := b.Reserve(t.Context(), make([]uint32, tt.nin), tt.nout); !errors.Is(err, batch.ErrRange) {
			t.Errorf("Reserve(%d in, %d out) = %v, want ErrRange", tt.nin, tt.nout, err)
		}
	}
	h, err := b.Reserve(t.Context(), nil, 4) // no input is fine
	if err != nil {
		t.Fatalf("Reserve(nil, 4) = %v, want nil", err)
	}
	h.Done()
	var zero batch.Handle[uint32, float32]
	if out, err := zero.Wait(t.Context()); out != nil || err != nil {
		t.Errorf("zero Handle's Wait() = %v, %v, want nil, nil", out, err)
	}
	zero.Done()
}

func TestInputs(t *testing.T) {
	// Callers bring token IDs and want floats back, in their own amounts.
	// The owner finds each caller's input and output by the offsets, and
	// no batch sees an earlier batch's data.
	b := batch.New[uint32, float32](8, 8, 2)
	ctx := context.Background()
	for round := range 3 {
		x := []uint32{1, 2, 3}
		h1, err := b.Reserve(ctx, x, 2)
		if err != nil {
			t.Fatal(err)
		}
		x[0] = 99 // Reserve copied x, and doesn't keep it
		h2, err := b.Reserve(ctx, []uint32{10}, 3)
		if err != nil {
			t.Fatal(err)
		}
		bt := runOne(ctx, b, func(bt batch.Batch[uint32, float32]) {
			for i := range len(bt.InOffsets) - 1 {
				var sum uint32
				for _, v := range bt.In[bt.InOffsets[i]:bt.InOffsets[i+1]] {
					sum += v
				}
				out := bt.Out[bt.OutOffsets[i]:bt.OutOffsets[i+1]]
				for j := range out {
					out[j] += float32(sum) // += shows Out started zeroed
				}
			}
		})
		if !slices.Equal(bt.In, []uint32{1, 2, 3, 10}) ||
			!slices.Equal(bt.InOffsets, []int{0, 3, 4}) ||
			!slices.Equal(bt.OutOffsets, []int{0, 2, 5}) {
			t.Errorf("round %d: batch = %+v, want In [1 2 3 10], InOffsets [0 3 4], OutOffsets [0 2 5]", round, bt)
		}
		r1, _ := h1.Wait(ctx)
		r2, _ := h2.Wait(ctx)
		if !slices.Equal(r1, []float32{6, 6}) || !slices.Equal(r2, []float32{10, 10, 10}) {
			t.Errorf("round %d: results = %v, %v, want [6 6], [10 10 10]", round, r1, r2)
		}
		h1.Done()
		h2.Done()
	}
}

func TestHandle(t *testing.T) {
	b := batch.New[float32, float32](4, 4, 2)
	ctx := context.Background()
	h1, err := b.Reserve(ctx, in(2), 2)
	if err != nil {
		t.Fatalf("Reserve(2) = %v, want nil", err)
	}
	h2, err := b.Reserve(ctx, in(2), 2)
	if err != nil {
		t.Fatalf("Reserve(2) = %v, want nil", err)
	}
	mem := runOne(ctx, b, func(batch.Batch[float32, float32]) {}).Out
	m1, err1 := h1.Wait(ctx)
	m2, err2 := h2.Wait(ctx)
	if err1 != nil || err2 != nil || &m1[0] != &mem[0] || &m2[0] != &mem[2] {
		t.Fatalf("Handles hold %v, %v and %v, %v, want subslices of %v", m1, err1, m2, err2, mem)
	}
	if cap(m1) != 2 || cap(m2) != 2 || cap(mem) != 4 {
		t.Errorf("caps = %d, %d, %d, want 2, 2, 4", cap(m1), cap(m2), cap(mem))
	}
}

// runOne runs f on b's next batch and returns the batch.
func runOne[In, Out any](ctx context.Context, b *batch.Batcher[In, Out], f func(batch.Batch[In, Out])) batch.Batch[In, Out] {
	for bt := range b.Batches(ctx) {
		f(bt)
		return bt
	}
	return batch.Batch[In, Out]{}
}

func TestNewPanic(t *testing.T) {
	for _, tt := range []struct{ in, out, buffers int }{
		{-1, 1, 2},
		{1, 0, 2},
		{1, -1, 2},
		{1, 1, 1},
		{1, 1, 0},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("New(%d, %d, %d) did not panic", tt.in, tt.out, tt.buffers)
				}
			}()
			batch.New[uint32, float32](tt.in, tt.out, tt.buffers)
		}()
	}
}

func TestAllocs(t *testing.T) {
	ctx := context.Background()
	b := batch.New[float32, float32](1024, 1024, 2)
	go func() {
		for range b.Batches(ctx) {
		}
	}()
	defer b.Close()
	x := in(1)
	n := testing.AllocsPerRun(100, func() {
		h, err := b.Reserve(ctx, x, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		h.Done()
	})
	if n != 0 {
		t.Errorf("a batch of one allocates %v times, want 0", n)
	}
}

// BenchmarkReserve runs parallel callers, each reserving n elements and
// waiting for its results, against owners ranging over Batches in their
// own goroutines. batches/op is the number of batches run per caller.
func BenchmarkReserve(b *testing.B) {
	for _, bc := range []struct {
		size, n, owners int
		delay           time.Duration
	}{
		{64, 1, 1, 0},
		{4096, 1, 1, 0},
		{65536, 1, 1, 0},
		{4096, 16, 1, 0},
		{4096, 1, 4, 0},
		{4096, 1, 1, 100 * time.Microsecond},
	} {
		name := fmt.Sprintf("size=%d/n=%d/owners=%d/delay=%v", bc.size, bc.n, bc.owners, bc.delay)
		b.Run(name, func(b *testing.B) {
			ctx := context.Background()
			bt := batch.New[float32, float32](bc.size, bc.size, bc.owners+1)
			bt.MaxDelay = bc.delay
			var batches atomic.Int64
			var owners sync.WaitGroup
			for range bc.owners {
				owners.Go(func() {
					for range bt.Batches(ctx) {
						batches.Add(1)
					}
				})
			}
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				x := in(bc.n)
				for pb.Next() {
					h, err := bt.Reserve(ctx, x, bc.n)
					if err != nil {
						b.Error(err)
						return
					}
					if _, err := h.Wait(ctx); err != nil {
						b.Error(err)
						return
					}
					h.Done()
				}
			})
			bt.Close()
			owners.Wait()
			b.ReportMetric(float64(batches.Load())/float64(b.N), "batches/op")
		})
	}
}
