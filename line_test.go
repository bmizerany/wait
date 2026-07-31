package wait

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// testLine returns a Line admitting int demands against capacity
// total, plus a func reporting the current free capacity. The free
// counter is mutated only under the Line's lock; tests read it after
// synctest.Wait, when the bubble is quiesced.
func testLine(total int) (*Line[int], func() int) {
	free := total
	l := &Line[int]{
		Fill: func(d int) bool {
			if d > free {
				return false
			}
			free -= d
			return true
		},
		Refill: func(d int) { free += d },
	}
	return l, func() int { return free }
}

func TestLine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, free := testLine(4)

		checkWait := func(d int) {
			t.Helper()
			if err := l.Wait(t.Context(), d); err != nil {
				t.Errorf("Wait(%d) = %v, want nil", d, err)
			}
		}
		checkFree := func(want int) {
			t.Helper()
			if got := free(); got != want {
				t.Errorf("free = %d, want %d", got, want)
			}
		}

		// An empty line admits fitting demands without queueing.
		checkWait(3)
		checkFree(1)
		checkWait(1)
		checkFree(0)

		// Put returns capacity for the next demand.
		l.Put(3)
		checkFree(3)
		checkWait(2)
		checkFree(1)

		l.Put(2)
		l.Put(1)
		checkFree(4)
	})
}

func TestLineZeroValue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l Line[string]

		// nil Fill admits everything; nil Refill is a no-op.
		for range 3 {
			if err := l.Wait(t.Context(), "anything"); err != nil {
				t.Fatalf("Wait = %v, want nil", err)
			}
		}
		if !l.TryWait("more") {
			t.Error("TryWait = false, want true")
		}
		l.Put("back")
	})
}

func TestLineStrictFIFO(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, free := testLine(4)

		// A takes most of the capacity.
		if err := l.Wait(t.Context(), 3); err != nil {
			t.Fatal("A Wait(3):", err)
		}

		// B does not fit and heads the line. C fits the free
		// capacity but must not pass B.
		var admitted [2]bool
		go func() {
			if err := l.Wait(t.Context(), 2); err != nil {
				t.Errorf("B Wait(2) = %v, want nil", err)
			}
			admitted[0] = true
		}()
		synctest.Wait()
		go func() {
			if err := l.Wait(t.Context(), 1); err != nil {
				t.Errorf("C Wait(1) = %v, want nil", err)
			}
			admitted[1] = true
		}()
		synctest.Wait()

		if admitted != [2]bool{} {
			t.Fatalf("admitted = %v, want none", admitted)
		}
		if got := free(); got != 1 {
			t.Fatalf("free = %d, want 1 (C must not fill ahead of B)", got)
		}

		// A releases: one Put admits B, then C, in order.
		l.Put(3)
		synctest.Wait()
		if admitted != [2]bool{true, true} {
			t.Fatalf("admitted = %v, want both", admitted)
		}
		if got := free(); got != 1 {
			t.Fatalf("free = %d, want 1", got)
		}
	})
}

func TestLinePutCascade(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, free := testLine(6)

		if err := l.Wait(t.Context(), 6); err != nil {
			t.Fatal("draining:", err)
		}

		var admitted [3]bool
		for i, d := range []int{2, 2, 3} {
			go func() {
				if err := l.Wait(t.Context(), d); err != nil {
					t.Errorf("waiter %d Wait(%d) = %v, want nil", i, d, err)
				}
				admitted[i] = true
			}()
			synctest.Wait()
		}

		// One Put admits waiters in order until the head no
		// longer fits: 2 and 2 admit, 3 stays at the head.
		l.Put(6)
		synctest.Wait()
		if want := [3]bool{true, true, false}; admitted != want {
			t.Fatalf("admitted = %v, want %v", admitted, want)
		}
		if got := free(); got != 2 {
			t.Fatalf("free = %d, want 2", got)
		}

		// Enough for the head; it admits.
		l.Put(1)
		synctest.Wait()
		if want := [3]bool{true, true, true}; admitted != want {
			t.Fatalf("admitted = %v, want %v", admitted, want)
		}
		if got := free(); got != 0 {
			t.Fatalf("free = %d, want 0", got)
		}
	})
}

func TestLineTryWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, free := testLine(4)

		// Empty line: TryWait admits what fits.
		if !l.TryWait(3) {
			t.Fatal("TryWait(3) = false, want true")
		}
		if l.TryWait(2) {
			t.Fatal("TryWait(2) = true, want false (only 1 free)")
		}

		// A waiter joins the line. TryWait must decline even though
		// its demand fits: it never cuts the line.
		go func() {
			if err := l.Wait(t.Context(), 2); err != nil {
				t.Errorf("Wait(2) = %v, want nil", err)
			}
		}()
		synctest.Wait()
		if l.TryWait(1) {
			t.Fatal("TryWait(1) = true, want false (a waiter is in line)")
		}

		// The waiter admits and the line empties; TryWait works again.
		l.Put(3)
		synctest.Wait()
		if got := free(); got != 2 {
			t.Fatalf("free = %d, want 2", got)
		}
		if !l.TryWait(2) {
			t.Fatal("TryWait(2) = false, want true")
		}
	})
}

func TestLineWaitContextCancel(t *testing.T) {
	t.Run("early cancel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l, free := testLine(1)

			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			if err := l.Wait(ctx, 1); !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, want context.Canceled", err)
			}
			if got := free(); got != 1 {
				t.Errorf("free = %d, want 1 (nothing deducted)", got)
			}
		})
	})

	t.Run("waiting cancel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l, free := testLine(1)

			if err := l.Wait(t.Context(), 1); err != nil {
				t.Fatal("draining:", err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			go func() {
				if err := l.Wait(ctx, 1); !errors.Is(err, context.Canceled) {
					t.Errorf("Wait = %v, want context.Canceled", err)
				}
			}()
			synctest.Wait()

			cancel()
			synctest.Wait()

			// The canceled waiter left the line without a grant.
			l.Put(1)
			if got := free(); got != 1 {
				t.Fatalf("free = %d, want 1", got)
			}
			if !l.TryWait(1) {
				t.Fatal("TryWait(1) = false, want true (line should be empty)")
			}
		})
	})

	t.Run("mid-queue cancel is skipped over", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l, free := testLine(3)

			if err := l.Wait(t.Context(), 3); err != nil {
				t.Fatal("draining:", err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			var admitted [3]bool
			for i, d := range []int{1, 2, 1} {
				go func() {
					wctx := t.Context()
					if i == 1 {
						wctx = ctx
					}
					err := l.Wait(wctx, d)
					if i == 1 {
						if !errors.Is(err, context.Canceled) {
							t.Errorf("waiter %d Wait = %v, want context.Canceled", i, err)
						}
						return
					}
					if err != nil {
						t.Errorf("waiter %d Wait(%d) = %v, want nil", i, d, err)
					}
					admitted[i] = true
				}()
				synctest.Wait()
			}

			// Cancel the middle waiter; the others keep their spots.
			cancel()
			synctest.Wait()

			l.Put(3)
			synctest.Wait()
			if want := [3]bool{true, false, true}; admitted != want {
				t.Fatalf("admitted = %v, want %v", admitted, want)
			}
			if got := free(); got != 1 {
				t.Fatalf("free = %d, want 1", got)
			}
		})
	})

	t.Run("head cancel unblocks a fitting successor", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l, free := testLine(3)

			if err := l.Wait(t.Context(), 1); err != nil {
				t.Fatal("A Wait(1):", err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			// B heads the line, too big for the 2 free. C fits but
			// waits behind B.
			go func() {
				if err := l.Wait(ctx, 3); !errors.Is(err, context.Canceled) {
					t.Errorf("B Wait(3) = %v, want context.Canceled", err)
				}
			}()
			synctest.Wait()

			var admitted bool
			go func() {
				if err := l.Wait(t.Context(), 2); err != nil {
					t.Errorf("C Wait(2) = %v, want nil", err)
				}
				admitted = true
			}()
			synctest.Wait()

			if admitted {
				t.Fatal("C admitted behind a blocked head")
			}

			// B leaves; C is the head now and fits — no Put needed.
			cancel()
			synctest.Wait()

			if !admitted {
				t.Fatal("C not admitted after head canceled")
			}
			if got := free(); got != 0 {
				t.Fatalf("free = %d, want 0", got)
			}
		})
	})
}

// TestLineNearMiss tests the near-miss scenario where an admission
// arrives just as the context is being canceled. Unlike List, which
// hands the raced value to the caller, a canceled Wait refunds the
// raced grant and reports the cancellation. This test uses the
// internal testHookLineWaiterCanceled field to reliably induce the
// race condition.
func TestLineNearMiss(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l, free := testLine(1)

		// Induce the near miss: admit the canceling waiter the
		// instant it begins handling its cancellation.
		l.testHookLineWaiterCanceled = func() { l.Put(1) }

		if err := l.Wait(t.Context(), 1); err != nil {
			t.Fatal("draining:", err)
		}

		errStop := errors.New("stop")
		ctx, cancel := context.WithCancelCause(t.Context())

		go func() {
			if err := l.Wait(ctx, 1); !errors.Is(err, errStop) {
				t.Errorf("Wait = %v, want errStop", err)
			}
		}()
		synctest.Wait()

		cancel(errStop)
		synctest.Wait()

		// The raced grant was refunded: the unit the hook returned
		// is free again, not leaked to a waiter that gave up.
		if got := free(); got != 1 {
			t.Fatalf("free = %d, want 1", got)
		}
		if !l.TryWait(1) {
			t.Fatal("TryWait(1) = false, want true (line should be empty)")
		}
	})
}

func TestLineClose(t *testing.T) {
	t.Run("unblocks waiters", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l, free := testLine(1)

			if err := l.Wait(t.Context(), 1); err != nil {
				t.Fatal("draining:", err)
			}

			var inflight atomic.Int64
			for range 3 {
				inflight.Add(1)
				go func() {
					defer inflight.Add(-1)
					if err := l.Wait(t.Context(), 1); !errors.Is(err, ErrClosed) {
						t.Errorf("Wait err = %v, want ErrClosed", err)
					}
				}()
			}
			synctest.Wait()

			if got := inflight.Load(); got != 3 {
				t.Fatalf("inflight = %d, want 3", got)
			}

			l.Close()
			synctest.Wait()

			if got := inflight.Load(); got != 0 {
				t.Fatalf("inflight = %d, want 0", got)
			}
			if got := free(); got != 0 {
				t.Fatalf("free = %d, want 0 (closed waiters never deducted)", got)
			}
		})
	})

	t.Run("wait after", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l, _ := testLine(1)

			l.Close()

			if err := l.Wait(t.Context(), 1); !errors.Is(err, ErrClosed) {
				t.Errorf("Wait err = %v, want ErrClosed", err)
			}
			if l.TryWait(1) {
				t.Error("TryWait after Close = true, want false")
			}
		})
	})

	t.Run("put after close still refills", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l, free := testLine(1)

			if err := l.Wait(t.Context(), 1); err != nil {
				t.Fatal("draining:", err)
			}

			l.Close()

			// The accounting belongs to the caller; a release
			// during shutdown must still land.
			l.Put(1)
			if got := free(); got != 1 {
				t.Fatalf("free = %d, want 1", got)
			}
		})
	})

	t.Run("idempotent", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l, _ := testLine(1)

			l.Close()
			l.Close()
			l.Close()

			if err := l.Wait(t.Context(), 1); !errors.Is(err, ErrClosed) {
				t.Errorf("Wait err = %v, want ErrClosed", err)
			}
		})
	})
}

// TestLineFairness admits mixed-size demands and requires service in
// exact join order, no matter how capacity trickles back. Admission
// order is observed from inside Fill, which runs under the Line's lock
// in exactly admission order.
func TestLineFairness(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		type demand struct{ id, size int }

		const total = 10
		free := total
		var order []int
		l := &Line[demand]{
			Fill: func(d demand) bool {
				if d.size > free {
					return false
				}
				free -= d.size
				order = append(order, d.id)
				return true
			},
			Refill: func(d demand) { free += d.size },
		}

		// Occupy everything so every waiter queues.
		if err := l.Wait(t.Context(), demand{id: -1, size: total}); err != nil {
			t.Fatal("draining:", err)
		}

		// Mixed sizes, several larger than their successors, one as
		// big as the whole line. Join order is pinned by waiting for
		// each waiter to durably block before starting the next.
		sizes := []int{5, 1, 9, 2, 10, 1, 3, 7}
		for i, size := range sizes {
			go func() {
				d := demand{id: i, size: size}
				if err := l.Wait(t.Context(), d); err != nil {
					t.Errorf("waiter %d Wait = %v, want nil", i, err)
					return
				}
				l.Put(d)
			}()
			synctest.Wait()
		}

		// Release the line and let the admissions cascade; each
		// waiter returns its capacity as it goes.
		l.Put(demand{id: -1, size: total})
		synctest.Wait()

		want := []int{-1, 0, 1, 2, 3, 4, 5, 6, 7}
		if !slices.Equal(order, want) {
			t.Fatalf("admission order = %v, want %v", order, want)
		}
		if free != total {
			t.Fatalf("free = %d, want %d", free, total)
		}
	})
}

func BenchmarkLine(b *testing.B) {
	b.Run("uncontended", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			free := 1
			l := &Line[int]{
				Fill: func(d int) bool {
					if d > free {
						return false
					}
					free -= d
					return true
				},
				Refill: func(d int) { free += d },
			}
			for pb.Next() {
				if err := l.Wait(context.Background(), 1); err != nil {
					b.Fatal("Wait:", err)
				}
				l.Put(1)
			}
		})
	})

	b.Run("contended", func(b *testing.B) {
		b.ReportAllocs()

		var tttw atomic.Int64 // total-time-to-wait
		var tttp atomic.Int64 // total-time-to-put

		free := 10
		l := &Line[int]{
			Fill: func(d int) bool {
				if d > free {
					return false
				}
				free -= d
				return true
			},
			Refill: func(d int) { free += d },
		}

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				ttw := time.Now()
				if err := l.Wait(context.Background(), 1); err != nil {
					b.Fatal("Wait:", err)
				}
				tttw.Add(time.Since(ttw).Nanoseconds())

				// "work"
				time.Sleep(time.Millisecond)

				ttp := time.Now()
				l.Put(1)
				tttp.Add(time.Since(ttp).Nanoseconds())
			}
		})

		b.ReportMetric(float64(tttw.Load())/float64(b.N), "ns/wait")
		b.ReportMetric(float64(tttp.Load())/float64(b.N), "ns/put")
	})
}
