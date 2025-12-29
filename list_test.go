package wait

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestList(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &List[int]{
			MaxItems:   2,
			MaxWaiters: 3,
		}

		loads := new(atomic.Int64)
		checkTake := func(want int) {
			t.Helper()
			got, err := p.Take(t.Context(), func() int {
				return int(loads.Add(1) - 1)
			})
			if err != nil {
				if want < 0 {
					if !errors.Is(err, ErrMaxWaiters) {
						t.Errorf("err = %v, want ErrMaxWaiters", err)
					}
					return
				}
				t.Error("unexpected error taking from pool:", err)
			}
			if got != want {
				t.Errorf("got = %d, want %d", got, want)
			}
		}

		checkLoads := func(want int64) {
			t.Helper()
			if got := loads.Load(); got != want {
				t.Errorf("loads = %d, want %d", got, want)
			}
		}

		checkPutOK := func(v int) {
			t.Helper()
			if !p.Put(v) {
				t.Errorf("Put(%d) = false, want true", v)
			}
		}

		checkTake(0)
		checkPutOK(0)

		checkTake(0)
		checkTake(1)

		checkLoads(2)

		for i := range 3 {
			go checkTake(i % 2)
			synctest.Wait()
		}

		checkTake(-1) // unblocked still increments

		checkLoads(2)

		checkPutOK(0)
		synctest.Wait()
		checkLoads(2)

		checkPutOK(1)
		synctest.Wait()
		checkLoads(2)

		checkPutOK(0)
		synctest.Wait()
		checkLoads(2)

		p.Close()
		if p.Put(42) {
			t.Error("Put after Close = true, want false")
		}
	})
}

func TestListTakeContextCancel(t *testing.T) {
	// load funcs for testing
	shouldNotCall := func() int { panic("should not call load func") }
	fourtyTwo := func() int { return 42 }

	t.Run("early cancel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &List[int]{
				MaxItems:   1,
				MaxWaiters: 10,
			}

			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			_, err := p.Take(ctx, shouldNotCall)
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, want context.Canceled", err)
			}
		})
	})

	t.Run("early cancel with ready item", func(t *testing.T) {
		p := &List[int]{
			MaxItems:   1,
			MaxWaiters: 10,
		}

		// Put an item so it's ready
		p.Put(42)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// Should get the ready item even though context is canceled
		v, err := p.Take(ctx, shouldNotCall)
		if err != nil {
			t.Errorf("Take with canceled ctx but ready item: err = %v, want nil", err)
		}
		if v != 42 {
			t.Errorf("got %d, want 42", v)
		}
	})

	t.Run("waiting cancel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &List[int]{
				MaxItems:   1,
				MaxWaiters: 10,
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			// Drain to force waiters to durably block.
			_, err := p.Take(ctx, fourtyTwo)
			if err != nil {
				t.Fatal("draining:", err)
			}

			// 1. waiter starts waiting
			go func() {
				got, err := p.Take(ctx, shouldNotCall)
				if !errors.Is(err, context.Canceled) {
					t.Errorf("waiting cancel: err = %v, want context.Canceled (got = %v)", got, err)
				}
			}()

			// 2. waiter is durably blocked
			synctest.Wait()

			// 3. context is cancelled
			cancel()

			// 4. waiter sees cancellation
			synctest.Wait()
		})
	})
}

func BenchmarkList(b *testing.B) {
	b.Run("uncontended", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			p := &List[int]{MaxItems: 10}
			for pb.Next() {
				func() {
					v, err := p.Take(context.Background(), func() int { return 42 })
					if err != nil {
						b.Fatal("Take:", err)
					}
					if v != 42 {
						b.Fatalf("got %d, want 42", v)
					}
					p.Put(v)
				}()
			}
		})
	})

	b.Run("contended", func(b *testing.B) {
		b.ReportAllocs()

		var tttt atomic.Int64 // total-time-to-take
		var tttp atomic.Int64 // total-time-to-put

		p := &List[int]{
			MaxItems:   10,
			MaxWaiters: 100,
		}

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				func() {
					ttt := time.Now()
					h, err := p.Take(context.Background(), nil)
					if err != nil {
						b.Fatal("Take:", err)
					}
					tttt.Add(time.Since(ttt).Nanoseconds())

					// "work"
					time.Sleep(time.Millisecond)

					ttp := time.Now()
					p.Put(h)
					tttp.Add(time.Since(ttp).Nanoseconds())
				}()
			}
		})

		b.ReportMetric(float64(tttt.Load())/float64(b.N), "ns/take")
		b.ReportMetric(float64(tttp.Load())/float64(b.N), "ns/put")
	})
}

func TestWaitListClose(t *testing.T) {
	t.Run("put before and after", func(t *testing.T) {
		var p List[int]

		// Start multiple Puts and a Close concurrently
		for i := range 5 {
			p.Put(i)
		}

		p.Close()

		// After Close, new Puts should do nothing
		p.Put(999)

		for {
			v, ok := p.TryTake()
			if !ok {
				break
			}
			if v < 0 || v > 4 {
				t.Errorf("Got unexpected value %d from TryTake after Close", v)
			}
		}
	})

	t.Run("unblocks waiters", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &List[int]{
				MaxItems:   1,
				MaxWaiters: 3,
			}

			_, err := p.Take(t.Context(), nil)
			if err != nil {
				t.Fatal("Initial Take():", err)
			}

			var inflight atomic.Int64
			for range 3 {
				inflight.Add(1)
				go func() {
					defer inflight.Add(-1)
					_, err := p.Take(t.Context(), nil)
					if !errors.Is(err, ErrClosed) {
						t.Errorf("Take() err = %v, want ErrClosed", err)
					}
				}()
			}

			// Wait for the goroutine to block
			synctest.Wait()

			// Ensure all 3 are inflight after goroutines are durably blocked
			if got := inflight.Load(); got != 3 {
				t.Fatalf("inflight = %d, want 3", got)
			}

			// Close and let waiter goroutines check their own
			// errors as we exit the bubble.
			// If they remain blocked, synctest will panic.
			p.Close()

			synctest.Wait()

			if got := inflight.Load(); got != 0 {
				t.Fatalf("inflight = %d, want 0", got)
			}
		})
	})

	t.Run("wait after", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var p List[int]

			p.Close()

			_, err := p.Take(t.Context(), nil)
			if !errors.Is(err, ErrClosed) {
				t.Errorf("Take() err = %v, want ErrClosed", err)
			}
		})
	})

	t.Run("drain ready items after close", func(t *testing.T) {
		var p List[int]

		// Put several items
		for i := range 5 {
			p.Put(i)
		}

		// Close the list
		p.Close()

		// Should still be able to Take all ready items
		seen := make(map[int]bool)
		for i := 0; i < 5; i++ {
			v, err := p.Take(context.Background(), nil)
			if err != nil {
				t.Fatalf("Take after close (item %d): got err %v, want nil", i, err)
			}
			if v < 0 || v > 4 {
				t.Errorf("Got unexpected value %d from Take after Close", v)
			}
			seen[v] = true
		}

		if len(seen) != 5 {
			t.Errorf("Expected to see 5 unique values, got %d: %v", len(seen), seen)
		}

		// Now that all ready items are drained, should get ErrClosed
		_, err := p.Take(context.Background(), nil)
		if !errors.Is(err, ErrClosed) {
			t.Errorf("Take after draining: err = %v, want ErrClosed", err)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		var p List[int]

		// Close multiple times - should not panic
		p.Close()
		p.Close()
		p.Close()

		// Should still return ErrClosed
		_, err := p.Take(context.Background(), nil)
		if !errors.Is(err, ErrClosed) {
			t.Errorf("Take() err = %v, want ErrClosed", err)
		}
	})
}

// TestTakeNearMiss tests the near-miss scenario where a value arrives
// just as the context is being canceled. This test uses the internal
// testHookWaiterCanceled field to reliably induce the race condition.
func TestTakeNearMiss(t *testing.T) {
	shouldNotCall := func() int { panic("should not call load func") }
	fourtyTwo := func() int { return 42 }

	synctest.Test(t, func(t *testing.T) {
		p := &List[int]{
			MaxItems:   1,
			MaxWaiters: 10,

			// induce near miss
			testHookWaiterCanceled: func(ch chan int) { ch <- 42 },
		}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		// Drain to force waiters to durably block.
		_, err := p.Take(ctx, fourtyTwo)
		if err != nil {
			t.Fatal("draining:", err)
		}

		// 1. waiter starts waiting
		go func() {
			got, err := p.Take(ctx, shouldNotCall)
			if err != nil {
				t.Errorf("near miss recovery: %v", err)
			}
			defer p.Put(got)
			if got != 42 {
				t.Errorf("near miss recovery: got = %d, want 42", got)
			}
		}()
		synctest.Wait()

		// 2. context is cancelled
		cancel()

		// 3. waiter sees cancellation
		// 4. near miss happens
		// 5. waiter recovers and gets 42
		synctest.Wait()
	})
}
