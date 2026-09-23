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

// testGate returns a Gate admitting int demands against capacity
// total, plus a func reporting the current free capacity. The free
// counter is mutated only under the Gate's lock; tests read it after
// synctest.Wait, when the bubble is quiesced.
func testGate(total int) (*Gate[int], func() int) {
	free := total
	g := &Gate[int]{
		Claim: func(d int) bool {
			if d > free {
				return false
			}
			free -= d
			return true
		},
		Release: func(d int) { free += d },
	}
	return g, func() int { return free }
}

func TestGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, free := testGate(4)

		admit := func(d int) Grant[int] {
			t.Helper()
			grant, err := g.Wait(t.Context(), d)
			if err != nil {
				t.Fatalf("Wait(%d) = %v, want nil", d, err)
			}
			return grant
		}
		checkFree := func(want int) {
			t.Helper()
			if got := free(); got != want {
				t.Errorf("free = %d, want %d", got, want)
			}
		}

		// An empty line admits fitting demands without queueing.
		grant3 := admit(3)
		checkFree(1)
		grant1 := admit(1)
		checkFree(0)

		// Releasing returns capacity for the next demand.
		grant3.Release()
		checkFree(3)
		grant2 := admit(2)
		checkFree(1)

		grant2.Release()
		grant1.Release()
		checkFree(4)
	})
}

func TestGateZeroValue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var g Gate[string]

		// nil Claim admits everything; nil Release is a no-op.
		for range 3 {
			grant, err := g.Wait(t.Context(), "anything")
			if err != nil {
				t.Fatalf("Wait = %v, want nil", err)
			}
			grant.Release()
		}
		grant, ok := g.TryWait("more")
		if !ok {
			t.Fatal("TryWait = false, want true")
		}
		grant.Release()
	})
}

func TestGateStrictFIFO(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, free := testGate(4)

		// A takes most of the capacity.
		grantA, err := g.Wait(t.Context(), 3)
		if err != nil {
			t.Fatal("A Wait(3):", err)
		}

		// B does not fit and heads the line. C fits the free
		// capacity but must not pass B.
		var admitted [2]bool
		go func() {
			if _, err := g.Wait(t.Context(), 2); err != nil {
				t.Errorf("B Wait(2) = %v, want nil", err)
			}
			admitted[0] = true
		}()
		synctest.Wait()
		go func() {
			if _, err := g.Wait(t.Context(), 1); err != nil {
				t.Errorf("C Wait(1) = %v, want nil", err)
			}
			admitted[1] = true
		}()
		synctest.Wait()

		if admitted != [2]bool{} {
			t.Fatalf("admitted = %v, want none", admitted)
		}
		if got := free(); got != 1 {
			t.Fatalf("free = %d, want 1 (C must not claim ahead of B)", got)
		}

		// A releases: that one release admits B, then C, in order.
		grantA.Release()
		synctest.Wait()
		if admitted != [2]bool{true, true} {
			t.Fatalf("admitted = %v, want both", admitted)
		}
		if got := free(); got != 1 {
			t.Fatalf("free = %d, want 1", got)
		}
	})
}

func TestGateCascade(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, free := testGate(7)

		grant4, err := g.Wait(t.Context(), 4)
		if err != nil {
			t.Fatal("draining 4:", err)
		}
		grant3, err := g.Wait(t.Context(), 3)
		if err != nil {
			t.Fatal("draining 3:", err)
		}

		var admitted [3]bool
		for i, d := range []int{2, 2, 3} {
			go func() {
				if _, err := g.Wait(t.Context(), d); err != nil {
					t.Errorf("waiter %d Wait(%d) = %v, want nil", i, d, err)
				}
				admitted[i] = true
			}()
			synctest.Wait()
		}

		// One release admits waiters in order until the head no
		// longer fits: 2 and 2 admit, 3 stays at the head.
		grant4.Release()
		synctest.Wait()
		if want := [3]bool{true, true, false}; admitted != want {
			t.Fatalf("admitted = %v, want %v", admitted, want)
		}
		if got := free(); got != 0 {
			t.Fatalf("free = %d, want 0", got)
		}

		// Enough for the head; it admits.
		grant3.Release()
		synctest.Wait()
		if want := [3]bool{true, true, true}; admitted != want {
			t.Fatalf("admitted = %v, want %v", admitted, want)
		}
		if got := free(); got != 0 {
			t.Fatalf("free = %d, want 0", got)
		}
	})
}

func TestGateTryWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, free := testGate(4)

		// Empty line: TryWait admits what fits.
		grant, ok := g.TryWait(3)
		if !ok {
			t.Fatal("TryWait(3) = false, want true")
		}
		if _, ok := g.TryWait(2); ok {
			t.Fatal("TryWait(2) = true, want false (only 1 free)")
		}

		// A waiter joins the line. TryWait must decline even though
		// its demand fits: it never cuts the line.
		go func() {
			if _, err := g.Wait(t.Context(), 2); err != nil {
				t.Errorf("Wait(2) = %v, want nil", err)
			}
		}()
		synctest.Wait()
		if _, ok := g.TryWait(1); ok {
			t.Fatal("TryWait(1) = true, want false (a waiter is in line)")
		}

		// The waiter admits and the line empties; TryWait works again.
		grant.Release()
		synctest.Wait()
		if got := free(); got != 2 {
			t.Fatalf("free = %d, want 2", got)
		}
		if _, ok := g.TryWait(2); !ok {
			t.Fatal("TryWait(2) = false, want true")
		}
	})
}

func TestGateReleaseTwice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, free := testGate(2)

		grant, ok := g.TryWait(2)
		if !ok {
			t.Fatal("TryWait(2) = false, want true")
		}
		grant.Release()
		grant.Release()
		if got := free(); got != 2 {
			t.Fatalf("free after TryWait release twice = %d, want 2", got)
		}

		grant, err := g.Wait(t.Context(), 2)
		if err != nil {
			t.Fatal("Wait(2):", err)
		}
		var admitted bool
		go func() {
			if _, err := g.Wait(t.Context(), 2); err != nil {
				t.Errorf("waiter Wait(2) = %v, want nil", err)
			}
			admitted = true
		}()
		synctest.Wait()

		grant.Release() // admits the waiter
		grant.Release() // must not credit capacity the waiter now holds
		synctest.Wait()
		if !admitted {
			t.Fatal("waiter not admitted after release")
		}
		if got := free(); got != 0 {
			t.Fatalf("free after Wait release twice = %d, want 0", got)
		}
	})
}

func TestGateStaleGrant(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, free := testGate(1)

		old, err := g.Wait(t.Context(), 1)
		if err != nil {
			t.Fatal("Wait old:", err)
		}
		old.Release()
		cur, err := g.Wait(t.Context(), 1) // usually reuses old's slot
		if err != nil {
			t.Fatal("Wait cur:", err)
		}
		dup := cur

		old.Release() // must not release cur
		if got := free(); got != 0 {
			t.Fatalf("free after stale Release = %d, want 0", got)
		}
		dup.Release()
		cur.Release() // dup already released it
		if got := free(); got != 1 {
			t.Fatalf("free after releasing cur and dup = %d, want 1", got)
		}
	})
}

func TestGateZeroGrant(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, free := testGate(1)

		var zero Grant[int]
		zero.Release()

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		grant, err := g.Wait(ctx, 1)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Wait = %v, want context.Canceled", err)
		}
		grant.Release()
		if got := free(); got != 1 {
			t.Fatalf("free after releasing a failed Wait's Grant = %d, want 1", got)
		}
	})
}

func TestGateAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("sync.Pool drops items at random under the race detector")
	}
	g, _ := testGate(1)
	ctx := context.Background()
	n := testing.AllocsPerRun(100, func() {
		grant, err := g.Wait(ctx, 1)
		if err != nil {
			t.Fatal("Wait:", err)
		}
		grant.Release()
		grant, ok := g.TryWait(1)
		if !ok {
			t.Fatal("TryWait(1) = false, want true")
		}
		grant.Release()
	})
	if n != 0 {
		t.Errorf("Wait, TryWait, and Release allocate %v times per run, want 0", n)
	}
}

func TestGateWaitContextCancel(t *testing.T) {
	t.Run("early cancel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			g, free := testGate(1)

			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			if _, err := g.Wait(ctx, 1); !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, want context.Canceled", err)
			}
			if got := free(); got != 1 {
				t.Errorf("free = %d, want 1 (nothing deducted)", got)
			}
		})
	})

	t.Run("waiting cancel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			g, free := testGate(1)

			grant, err := g.Wait(t.Context(), 1)
			if err != nil {
				t.Fatal("draining:", err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			go func() {
				if _, err := g.Wait(ctx, 1); !errors.Is(err, context.Canceled) {
					t.Errorf("Wait = %v, want context.Canceled", err)
				}
			}()
			synctest.Wait()

			cancel()
			synctest.Wait()

			// The canceled waiter left the line without a grant.
			grant.Release()
			if got := free(); got != 1 {
				t.Fatalf("free = %d, want 1", got)
			}
			if _, ok := g.TryWait(1); !ok {
				t.Fatal("TryWait(1) = false, want true (line should be empty)")
			}
		})
	})

	t.Run("mid-queue cancel is skipped over", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			g, free := testGate(3)

			grant, err := g.Wait(t.Context(), 3)
			if err != nil {
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
					_, err := g.Wait(wctx, d)
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

			grant.Release()
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
			g, free := testGate(3)

			if _, err := g.Wait(t.Context(), 1); err != nil {
				t.Fatal("A Wait(1):", err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			// B heads the line, too big for the 2 free. C fits but
			// waits behind B.
			go func() {
				if _, err := g.Wait(ctx, 3); !errors.Is(err, context.Canceled) {
					t.Errorf("B Wait(3) = %v, want context.Canceled", err)
				}
			}()
			synctest.Wait()

			var admitted bool
			go func() {
				if _, err := g.Wait(t.Context(), 2); err != nil {
					t.Errorf("C Wait(2) = %v, want nil", err)
				}
				admitted = true
			}()
			synctest.Wait()

			if admitted {
				t.Fatal("C admitted behind a blocked head")
			}

			// B leaves; C is the head now and fits, with no release.
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

// TestGateNearMiss tests the near-miss scenario where an admission
// arrives just as the context is being canceled. Unlike List, which
// hands the raced value to the caller, a canceled Wait releases the
// raced grant and reports the cancellation. This test uses the
// internal testHookCanceled field to reliably induce the
// race condition.
func TestGateNearMiss(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, free := testGate(1)

		grant, err := g.Wait(t.Context(), 1)
		if err != nil {
			t.Fatal("draining:", err)
		}
		// Induce the near miss: admit the canceling waiter the
		// instant it begins handling its cancellation.
		g.waiters.testHookCanceled = grant.Release

		errStop := errors.New("stop")
		ctx, cancel := context.WithCancelCause(t.Context())

		go func() {
			if _, err := g.Wait(ctx, 1); !errors.Is(err, errStop) {
				t.Errorf("Wait = %v, want errStop", err)
			}
		}()
		synctest.Wait()

		cancel(errStop)
		synctest.Wait()

		// The raced grant was released: the unit the hook returned
		// is free again, not leaked to a waiter that gave up.
		if got := free(); got != 1 {
			t.Fatalf("free = %d, want 1", got)
		}
		if _, ok := g.TryWait(1); !ok {
			t.Fatal("TryWait(1) = false, want true (line should be empty)")
		}
	})
}

func TestGateClose(t *testing.T) {
	t.Run("unblocks waiters", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			g, free := testGate(1)

			if _, err := g.Wait(t.Context(), 1); err != nil {
				t.Fatal("draining:", err)
			}

			var inflight atomic.Int64
			for range 3 {
				inflight.Add(1)
				go func() {
					defer inflight.Add(-1)
					if _, err := g.Wait(t.Context(), 1); !errors.Is(err, ErrClosed) {
						t.Errorf("Wait err = %v, want ErrClosed", err)
					}
				}()
			}
			synctest.Wait()

			if got := inflight.Load(); got != 3 {
				t.Fatalf("inflight = %d, want 3", got)
			}

			g.Close()
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
			g, _ := testGate(1)

			g.Close()

			if _, err := g.Wait(t.Context(), 1); !errors.Is(err, ErrClosed) {
				t.Errorf("Wait err = %v, want ErrClosed", err)
			}
			if _, ok := g.TryWait(1); ok {
				t.Error("TryWait after Close = true, want false")
			}
		})
	})

	t.Run("release after", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			g, free := testGate(1)

			grant, err := g.Wait(t.Context(), 1)
			if err != nil {
				t.Fatal("draining:", err)
			}

			g.Close()

			// The accounting belongs to the caller; a release
			// during shutdown must still land.
			grant.Release()
			if got := free(); got != 1 {
				t.Fatalf("free = %d, want 1", got)
			}
		})
	})

	t.Run("idempotent", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			g, _ := testGate(1)

			g.Close()
			g.Close()
			g.Close()

			if _, err := g.Wait(t.Context(), 1); !errors.Is(err, ErrClosed) {
				t.Errorf("Wait err = %v, want ErrClosed", err)
			}
		})
	})
}

// TestGateFairness admits mixed-size demands and requires service in
// exact join order, no matter how capacity trickles back. Admission
// order is observed from inside Claim, which runs under the Gate's lock
// in exactly admission order.
func TestGateFairness(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		type demand struct{ id, size int }

		const total = 10
		free := total
		var order []int
		g := &Gate[demand]{
			Claim: func(d demand) bool {
				if d.size > free {
					return false
				}
				free -= d.size
				order = append(order, d.id)
				return true
			},
			Release: func(d demand) { free += d.size },
		}

		// Occupy everything so every waiter queues.
		grant, err := g.Wait(t.Context(), demand{id: -1, size: total})
		if err != nil {
			t.Fatal("draining:", err)
		}

		// Mixed sizes, several larger than their successors, one as
		// big as the whole line. Join order is pinned by waiting for
		// each waiter to durably block before starting the next.
		sizes := []int{5, 1, 9, 2, 10, 1, 3, 7}
		for i, size := range sizes {
			go func() {
				d := demand{id: i, size: size}
				grant, err := g.Wait(t.Context(), d)
				if err != nil {
					t.Errorf("waiter %d Wait = %v, want nil", i, err)
					return
				}
				grant.Release()
			}()
			synctest.Wait()
		}

		// Release the line and let the admissions cascade; each
		// waiter returns its capacity as it goes.
		grant.Release()
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

func BenchmarkGate(b *testing.B) {
	b.Run("uncontended", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			free := 1
			g := &Gate[int]{
				Claim: func(d int) bool {
					if d > free {
						return false
					}
					free -= d
					return true
				},
				Release: func(d int) { free += d },
			}
			for pb.Next() {
				grant, err := g.Wait(context.Background(), 1)
				if err != nil {
					b.Fatal("Wait:", err)
				}
				grant.Release()
			}
		})
	})

	b.Run("contended", func(b *testing.B) {
		b.ReportAllocs()

		var tttw atomic.Int64 // total-time-to-wait
		var tttr atomic.Int64 // total-time-to-release

		free := 10
		g := &Gate[int]{
			Claim: func(d int) bool {
				if d > free {
					return false
				}
				free -= d
				return true
			},
			Release: func(d int) { free += d },
		}

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				ttw := time.Now()
				grant, err := g.Wait(context.Background(), 1)
				if err != nil {
					b.Fatal("Wait:", err)
				}
				tttw.Add(time.Since(ttw).Nanoseconds())

				// "work"
				time.Sleep(time.Millisecond)

				ttr := time.Now()
				grant.Release()
				tttr.Add(time.Since(ttr).Nanoseconds())
			}
		})

		b.ReportMetric(float64(tttw.Load())/float64(b.N), "ns/wait")
		b.ReportMetric(float64(tttr.Load())/float64(b.N), "ns/release")
	})
}
