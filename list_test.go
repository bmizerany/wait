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

func TestListRetire(t *testing.T) {
	t.Run("frees capacity", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &List[int]{MaxItems: 1}

			var loads atomic.Int64
			got, err := p.Take(t.Context(), func() int {
				return int(loads.Add(1))
			})
			if err != nil {
				t.Fatal("initial Take():", err)
			}
			if got != 1 {
				t.Fatalf("initial Take() = %d, want 1", got)
			}

			p.Retire()

			got, err = p.Take(t.Context(), func() int {
				return int(loads.Add(1))
			})
			if err != nil {
				t.Fatal("replacement Take():", err)
			}
			if got != 2 {
				t.Fatalf("replacement Take() = %d, want 2", got)
			}
		})
	})

	t.Run("with blocked waiter starts one background load immediately", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &List[int]{MaxItems: 1}
			defer func() {
				p.Close()
				synctest.Wait()
			}()

			// occupy the only item so the next Take() will block
			_, err := p.Take(t.Context(), nil)
			if err != nil {
				t.Fatal("initial Take():", err)
			}

			// start a waiter that will block until Retire() and count loads
			var started atomic.Int64
			go func() {
				v, err := p.Take(t.Context(), func() int {
					started.Add(1)
					return 42
				})
				if err != nil {
					t.Errorf("waiting Take(): %v", err)
					return
				}
				if v != 42 {
					t.Errorf("waiting Take() got %d, want 2", v)
				}
			}()
			synctest.Wait()

			// Start two waiters whose loads would start
			// immediately if Retire() starts more than one load.
			// We will check that their loads never start.
			for range 2 {
				go func() {
					_, err := p.Take(t.Context(), func() int { return 999 })
					if !errors.Is(err, ErrClosed) {
						t.Errorf("expected unblock due to closing, got err = %v", err)
					}
				}()
			}
			synctest.Wait()

			if got := started.Load(); got != 0 {
				t.Fatalf("loads started before Retire = %d, want 0", got)
			}

			p.Retire()
			synctest.Wait()

			if got := started.Load(); got != 1 {
				t.Fatalf("loads started after Retire = %d, want 1", got)
			}
		})
	})

	t.Run("uses oldest queued waiter's load", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &List[int]{MaxItems: 1, MaxWaiters: 2}

			_, err := p.Take(t.Context(), func() int { return 1 })
			if err != nil {
				t.Fatal("initial Take():", err)
			}

			var oldestLoads atomic.Int64
			oldestResult := make(chan int, 1)
			go func() {
				v, err := p.Take(t.Context(), func() int {
					oldestLoads.Add(1)
					return 101
				})
				if err != nil {
					t.Errorf("oldest waiter Take(): %v", err)
					return
				}
				oldestResult <- v
			}()
			synctest.Wait()

			var newerLoads atomic.Int64
			newerErr := make(chan error, 1)
			go func() {
				_, err := p.Take(t.Context(), func() int {
					newerLoads.Add(1)
					return 202
				})
				newerErr <- err
			}()
			synctest.Wait()

			p.Retire()
			synctest.Wait()

			if got := oldestLoads.Load(); got != 1 {
				t.Fatalf("oldest waiter loads = %d, want 1", got)
			}
			if got := newerLoads.Load(); got != 0 {
				t.Fatalf("newer waiter loads = %d, want 0", got)
			}
			if got := <-oldestResult; got != 101 {
				t.Fatalf("oldest waiter got %d, want 101", got)
			}

			p.Close()
			synctest.Wait()

			if err := <-newerErr; !errors.Is(err, ErrClosed) {
				t.Fatalf("newer waiter err = %v, want ErrClosed", err)
			}
		})
	})

	t.Run("preserves FIFO service order under multiple waiters", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &List[int]{MaxItems: 1, MaxWaiters: 3}

			_, err := p.Take(t.Context(), func() int { return 0 })
			if err != nil {
				t.Fatal("initial Take():", err)
			}

			served := make(chan int, 3)
			for want := 1; want <= 3; want++ {
				want := want
				go func() {
					got, err := p.Take(t.Context(), func() int { return want })
					if err != nil {
						t.Errorf("waiter %d Take(): %v", want, err)
						return
					}
					if got != want {
						t.Errorf("waiter %d got %d, want %d", want, got, want)
						return
					}
					served <- got
					p.Retire()
				}()
				synctest.Wait()
			}

			p.Retire()
			synctest.Wait()

			for want := 1; want <= 3; want++ {
				if got := <-served; got != want {
					t.Fatalf("service #%d = %d, want %d", want, got, want)
				}
				synctest.Wait()
			}
		})
	})

	t.Run("does not over-create", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &List[int]{MaxItems: 1, MaxWaiters: 3}

			_, err := p.Take(t.Context(), func() int { return 1 })
			if err != nil {
				t.Fatal("initial Take():", err)
			}

			release := make(chan struct{})
			var started atomic.Int64
			results := make(chan int, 3)
			errs := make(chan error, 3)

			for range 3 {
				go func() {
					v, err := p.Take(t.Context(), func() int {
						started.Add(1)
						<-release
						return 2
					})
					if err != nil {
						errs <- err
						return
					}
					results <- v
				}()
				synctest.Wait()
			}

			p.Retire()
			synctest.Wait()

			if got := started.Load(); got != 1 {
				t.Fatalf("loads started after one Retire = %d, want 1", got)
			}

			close(release)
			synctest.Wait()

			if got := <-results; got != 2 {
				t.Fatalf("loaded value = %d, want 2", got)
			}

			p.Close()
			synctest.Wait()

			for range 2 {
				if err := <-errs; !errors.Is(err, ErrClosed) {
					t.Fatalf("waiting Take() err = %v, want ErrClosed", err)
				}
			}
		})
	})

	t.Run("while closed does not start replacement and keeps ErrClosed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &List[int]{MaxItems: 1, MaxWaiters: 1}

			var started atomic.Int64
			_, err := p.Take(t.Context(), func() int {
				started.Add(1)
				return 1
			})
			if err != nil {
				t.Fatal("initial Take():", err)
			}

			waiterErr := make(chan error, 1)
			go func() {
				_, err := p.Take(t.Context(), func() int {
					started.Add(1)
					return 2
				})
				waiterErr <- err
			}()
			synctest.Wait()

			p.Close()
			p.Retire()
			synctest.Wait()

			if got := started.Load(); got != 1 {
				t.Fatalf("loads started = %d, want 1", got)
			}
			if err := <-waiterErr; !errors.Is(err, ErrClosed) {
				t.Fatalf("waiting Take() err = %v, want ErrClosed", err)
			}

			_, err = p.Take(context.Background(), func() int {
				started.Add(1)
				return 3
			})
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("Take() after Close = %v, want ErrClosed", err)
			}
			if got := started.Load(); got != 1 {
				t.Fatalf("loads started after closed Take = %d, want 1", got)
			}
		})
	})

	t.Run("replacement load can hand off after oldest waiter cancels", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &List[int]{MaxItems: 1, MaxWaiters: 2}

			_, err := p.Take(t.Context(), func() int { return 1 })
			if err != nil {
				t.Fatal("initial Take():", err)
			}

			release := make(chan struct{})
			waiter1Ctx, cancelWaiter1 := context.WithCancel(t.Context())
			defer cancelWaiter1()

			var waiter1Loads atomic.Int64
			waiter1Err := make(chan error, 1)
			go func() {
				_, err := p.Take(waiter1Ctx, func() int {
					waiter1Loads.Add(1)
					<-release
					return 11
				})
				waiter1Err <- err
			}()
			synctest.Wait()

			var waiter2Loads atomic.Int64
			waiter2Result := make(chan int, 1)
			go func() {
				v, err := p.Take(t.Context(), func() int {
					waiter2Loads.Add(1)
					return 22
				})
				if err != nil {
					t.Errorf("waiter 2 Take(): %v", err)
					return
				}
				waiter2Result <- v
			}()
			synctest.Wait()

			p.Retire()
			synctest.Wait()

			if got := waiter1Loads.Load(); got != 1 {
				t.Fatalf("waiter 1 loads = %d, want 1", got)
			}
			if got := waiter2Loads.Load(); got != 0 {
				t.Fatalf("waiter 2 loads before handoff = %d, want 0", got)
			}

			cancelWaiter1()
			synctest.Wait()

			close(release)
			synctest.Wait()

			if err := <-waiter1Err; !errors.Is(err, context.Canceled) {
				t.Fatalf("waiter 1 err = %v, want context.Canceled", err)
			}
			if got := <-waiter2Result; got != 11 {
				t.Fatalf("waiter 2 got %d, want 11", got)
			}
			if got := waiter2Loads.Load(); got != 0 {
				t.Fatalf("waiter 2 loads after handoff = %d, want 0", got)
			}
		})
	})
}
