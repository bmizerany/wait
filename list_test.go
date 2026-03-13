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
		p.New = func() int {
			return int(loads.Add(1) - 1)
		}
		checkTake := func(want int) {
			t.Helper()
			got, err := p.Take(t.Context())
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
	t.Run("early cancel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &List[int]{
				MaxItems:   1,
				MaxWaiters: 10,
				New:        func() int { panic("should not call load func") },
			}

			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			_, err := p.Take(ctx)
			if !errors.Is(err, context.Canceled) {
				t.Errorf("err = %v, want context.Canceled", err)
			}
		})
	})

	t.Run("early cancel with ready item", func(t *testing.T) {
		p := &List[int]{
			MaxItems:   1,
			MaxWaiters: 10,
			New:        func() int { panic("should not call load func") },
		}

		// Put an item so it's ready
		p.Put(42)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// Should get the ready item even though context is canceled
		v, err := p.Take(ctx)
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
				New:        func() int { return 42 },
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			// Drain to force waiters to durably block.
			_, err := p.Take(ctx)
			if err != nil {
				t.Fatal("draining:", err)
			}

			// 1. waiter starts waiting
			go func() {
				got, err := p.Take(ctx)
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
			p := &List[int]{
				MaxItems: 10,
				New:      func() int { return 42 },
			}
			for pb.Next() {
				func() {
					v, err := p.Take(context.Background())
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
			New:        func() int { return 0 },
		}

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				func() {
					ttt := time.Now()
					h, err := p.Take(context.Background())
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

			_, err := p.Take(t.Context())
			if err != nil {
				t.Fatal("Initial Take():", err)
			}

			var inflight atomic.Int64
			for range 3 {
				inflight.Add(1)
				go func() {
					defer inflight.Add(-1)
					_, err := p.Take(t.Context())
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

			_, err := p.Take(t.Context())
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
			v, err := p.Take(context.Background())
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
		_, err := p.Take(context.Background())
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
		_, err := p.Take(context.Background())
		if !errors.Is(err, ErrClosed) {
			t.Errorf("Take() err = %v, want ErrClosed", err)
		}
	})
}

// TestTakeNearMiss tests the near-miss scenario where a value arrives
// just as the context is being canceled. This test uses the internal
// testHookWaiterCanceled field to reliably induce the race condition.
func TestTakeNearMiss(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &List[int]{
			MaxItems:   1,
			MaxWaiters: 10,
			New:        func() int { return 42 },

			// induce near miss
			testHookWaiterCanceled: func(ch chan int) { ch <- 42 },
		}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		// Drain to force waiters to durably block.
		_, err := p.Take(ctx)
		if err != nil {
			t.Fatal("draining:", err)
		}

		// 1. waiter starts waiting
		go func() {
			got, err := p.Take(ctx)
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
			p.New = func() int {
				return int(loads.Add(1))
			}
			got, err := p.Take(t.Context())
			if err != nil {
				t.Fatal("initial Take():", err)
			}
			if got != 1 {
				t.Fatalf("initial Take() = %d, want 1", got)
			}

			p.Retire()

			got, err = p.Take(t.Context())
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
			var started atomic.Int64
			p := &List[int]{MaxItems: 1}
			defer func() {
				p.Close()
				synctest.Wait()
			}()

			// occupy the only item so the next Take() will block
			_, err := p.Take(t.Context())
			if err != nil {
				t.Fatal("initial Take():", err)
			}

			p.New = func() int {
				started.Add(1)
				return 42
			}

			// start a waiter that will block until Retire() and count loads
			go func() {
				v, err := p.Take(t.Context())
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
					_, err := p.Take(t.Context())
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
			var newCalls atomic.Int64
			p := &List[int]{
				MaxItems:   1,
				MaxWaiters: 2,
				New: func() int {
					switch newCalls.Add(1) {
					case 1:
						return 1
					default:
						return 101
					}
				},
			}

			_, err := p.Take(t.Context())
			if err != nil {
				t.Fatal("initial Take():", err)
			}

			var got [2]int
			go func() {
				v, err := p.Take(t.Context())
				if err != nil {
					t.Errorf("oldest waiter Take(): %v", err)
					return
				}
				got[0] = v
			}()
			synctest.Wait()

			go func() {
				_, err := p.Take(t.Context())
				if !errors.Is(err, ErrClosed) {
					t.Errorf("newer waiter err = %v, want ErrClosed", err)
				}
			}()
			synctest.Wait()

			p.Retire()
			synctest.Wait()

			if got := newCalls.Load(); got != 2 {
				t.Fatalf("New calls = %d, want 2", got)
			}
			if got[0] != 101 {
				t.Fatalf("oldest waiter got %d, want 101", got[0])
			}

			p.Close()
			synctest.Wait()
		})
	})

	t.Run("preserves FIFO service order under multiple waiters", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var nextNew atomic.Int64
			p := &List[int]{
				MaxItems:   1,
				MaxWaiters: 3,
				New: func() int {
					return int(nextNew.Add(1))
				},
			}

			_, err := p.Take(t.Context())
			if err != nil {
				t.Fatal("initial Take():", err)
			}

			var got [3]int
			for slot := range len(got) {
				go func() {
					v, err := p.Take(t.Context())
					if err != nil {
						t.Errorf("waiter %d Take(): %v", slot, err)
						return
					}
					got[slot] = v
					p.Retire()
				}()

				// Wait in turn for each waiter to get in line
				// before starting the next waiter, to ensure
				// they are queued in order.
				synctest.Wait()
			}

			p.Retire()
			synctest.Wait()

			want := [3]int{2, 3, 4}
			if got != want {
				t.Fatalf("got = %v, want %v", got, want)
			}
		})
	})

	t.Run("does not over-create", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var started atomic.Int64
			p := &List[int]{
				MaxItems:   1,
				MaxWaiters: 3,
				New: func() int {
					return int(started.Add(1))
				},
			}

			// Occupy only item on first Take() and start two more
			// waiters that will block and cause loads to start
			// when we Retire().
			var got [3]int
			for i := range 3 {
				go func() {
					v, err := p.Take(t.Context())
					if err != nil {
						t.Errorf("waiting Take() err = %v, want value", err)
						return
					}
					got[i] = v
				}()
				synctest.Wait()
			}

			synctest.Wait()
			want := [3]int{1, 0, 0}
			if got != want {
				t.Fatalf("before Retire got = %v, want %v", got, want)
			}

			p.Retire()
			synctest.Wait()
			want = [3]int{1, 2, 0}
			if got != want {
				t.Fatalf("after 1st Retire got = %v, want %v", got, want)
			}

			p.Put(42)
			synctest.Wait()
			want = [3]int{1, 2, 42}
			if got != want {
				t.Fatalf("after 2nd Retire got = %v, want %v", got, want)
			}

			synctest.Wait()
		})
	})

	t.Run("while closed does not start replacement and keeps ErrClosed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &List[int]{
				MaxItems:   1,
				MaxWaiters: 1,
				New: func() int {
					panic("should not start new load when closed")
				},
			}
			p.Close()
			p.Retire()
			p.Retire()

			_, err := p.Take(t.Context())
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("Take() err = %v, want ErrClosed", err)
			}
		})
	})

	t.Run("replacement load can hand off after oldest waiter cancels", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var newCalls atomic.Int64
			p := &List[int]{
				MaxItems:   1,
				MaxWaiters: 2,
				New: func() int {
					return int(newCalls.Add(1))
				},
			}

			// Force waiters by taking the only item.
			got, err := p.Take(t.Context())
			if err != nil {
				t.Fatal("initial Take():", err)
			}
			if got != 1 {
				t.Fatalf("initial Take() = %d, want 1", got)
			}

			oldestWaiterCtx, cancel := context.WithCancel(t.Context())
			defer cancel()

			// Start two waiters.
			// The first in line will be canceled, and the
			// replacement load will go to the next waiter.
			go func() {
				v, err := p.Take(oldestWaiterCtx)
				if !errors.Is(err, context.Canceled) {
					t.Errorf("oldest waiter Take() err = %v, want context.Canceled (got %v)", err, v)
				}
			}()
			synctest.Wait()

			go func() {
				got, err := p.Take(t.Context())
				if err != nil {
					t.Errorf("newer waiter Take() err = %v, want value", err)
					return
				}
				if got != 2 {
					t.Errorf("newer waiter Take() got %d, want 2", got)
				}
			}()
			synctest.Wait()

			// Cancel the oldest waiter, which should cause the
			// replacement load to skip it and go to the newer
			// waiter.
			cancel()
			synctest.Wait()

			// Retire to trigger replacement load, which should go
			// to newer waiter, not canceled oldest waiter.
			p.Retire()

			synctest.Wait()
		})
	})
}
