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
		l := &List[int]{
			MaxItems:   2,
			MaxWaiters: 3,
		}
		var loads atomic.Int64
		l.New = func() int { return int(loads.Add(1) - 1) }

		checkLoads := func(want int64) {
			t.Helper()
			if got := loads.Load(); got != want {
				t.Errorf("loads = %d, want %d", got, want)
			}
		}

		tk := held(t, l.Take(t.Context()))
		tk.Release() // item 0 is ready again

		a := held(t, l.Take(t.Context())) // item 0, reused
		b := held(t, l.Take(t.Context())) // item 1, created
		if v, _ := a.Value(); v != 0 {
			t.Errorf("a.Value() = %d, want 0", v)
		}
		if v, _ := b.Value(); v != 1 {
			t.Errorf("b.Value() = %d, want 1", v)
		}
		checkLoads(2)

		// MaxItems is reached: three Tickets wait, and a fourth is
		// turned away.
		var waiting [3]Ticket[int]
		for i := range waiting {
			waiting[i] = l.Take(t.Context())
		}
		if _, err := l.Take(t.Context()).Value(); !errors.Is(err, ErrMaxWaiters) {
			t.Errorf("fourth Value() = %v, want ErrMaxWaiters", err)
		}

		a.Release()
		b.Release()
		if !l.Add(2) {
			t.Error("Add(2) = false, want true")
		}
		for i, want := range []int{0, 1, 2} {
			if v, err := waiting[i].Value(); v != want || err != nil {
				t.Errorf("waiting[%d].Value() = %d, %v, want %d, nil", i, v, err, want)
			}
		}
		synctest.Wait()
		checkLoads(2)

		l.Close()
		if l.Add(42) {
			t.Error("Add after Close = true, want false")
		}
	})
}

func TestListTakeCancel(t *testing.T) {
	t.Run("early cancel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l := &List[int]{
				MaxItems: 1,
				New:      func() int { panic("should not call New") },
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			tk := l.Take(ctx)
			if !tk.Ready() {
				t.Error("Ready() = false, want true")
			}
			if _, err := tk.Value(); !errors.Is(err, context.Canceled) {
				t.Errorf("Value() = %v, want context.Canceled", err)
			}
		})
	})

	t.Run("early cancel with ready item", func(t *testing.T) {
		l := &List[int]{
			MaxItems: 1,
			New:      func() int { panic("should not call New") },
		}
		l.Add(42)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// The ready item wins over the canceled context.
		if v, err := l.Take(ctx).Value(); v != 42 || err != nil {
			t.Errorf("Value() = %d, %v, want 42, nil", v, err)
		}
	})

	t.Run("waiting cancel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l := &List[int]{MaxItems: 1}
			held(t, l.Take(t.Context())) // occupy the only item

			ctx, cancel := context.WithCancelCause(t.Context())
			tk := l.Take(ctx)
			go func() {
				if _, err := tk.Value(); !errors.Is(err, errStop) {
					t.Errorf("Value() = %v, want errStop", err)
				}
			}()
			synctest.Wait()
			if tk.Ready() {
				t.Error("Ready() = true while waiting, want false")
			}

			cancel(errStop)
			synctest.Wait()
		})
	})
}

var errStop = errors.New("stop")

func TestListTakeFIFO(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &List[int]{MaxItems: 1}
		held(t, l.Take(t.Context()))

		// Both Tickets are in line when Take returns.
		first := l.Take(t.Context())
		second := l.Take(t.Context())
		l.Add(11)
		l.Add(22)
		if v, err := first.Value(); v != 11 || err != nil {
			t.Errorf("first.Value() = %d, %v, want 11, nil", v, err)
		}
		if v, err := second.Value(); v != 22 || err != nil {
			t.Errorf("second.Value() = %d, %v, want 22, nil", v, err)
		}
	})
}

func TestListSkipsCanceled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &List[int]{MaxItems: 1, MaxWaiters: 1}
		held(t, l.Take(t.Context()))

		// No one waits on canceled's Value, yet it must give up its
		// MaxWaiters place and its turn.
		ctx, cancel := context.WithCancelCause(t.Context())
		canceled := l.Take(ctx)
		cancel(errStop)

		next := l.Take(t.Context())
		if next.Ready() {
			t.Fatalf("next Ready() = true, want false (canceled Ticket still holds its place)")
		}
		l.Add(33)
		if v, err := next.Value(); v != 33 || err != nil {
			t.Errorf("next.Value() = %d, %v, want 33, nil", v, err)
		}
		if _, err := canceled.Value(); err != errStop {
			t.Errorf("canceled.Value() = %v, want errStop", err)
		}
	})
}

func TestListSkipsCanceledValue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &List[int]{MaxItems: 1, New: func() int { return 42 }}
		holder := held(t, l.Take(t.Context()))
		// Give the item back after ctx is canceled but before Value
		// notices. It must go to the ready stack, not the canceled Ticket.
		l.waiters.testHookCanceled = holder.Release

		ctx, cancel := context.WithCancel(t.Context())
		tk := l.Take(ctx)
		go func() {
			if _, err := tk.Value(); !errors.Is(err, context.Canceled) {
				t.Errorf("Value() = %v, want context.Canceled", err)
			}
		}()
		synctest.Wait()

		cancel()
		synctest.Wait()
		if v, err := l.Take(done).Value(); v != 42 || err != nil {
			t.Errorf("Take(done).Value() = %d, %v, want the released item 42, nil", v, err)
		}
	})
}

func TestListAdmissionWins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &List[int]{MaxItems: 1, New: func() int { return 44 }}
		holder := held(t, l.Take(t.Context()))

		ctx, cancel := context.WithCancel(t.Context())
		tk := l.Take(ctx)
		holder.Release() // admits tk
		cancel()
		if v, err := tk.Value(); v != 44 || err != nil {
			t.Errorf("Value() = %d, %v, want 44, nil", v, err)
		}
	})
}

func TestListCloseWaiting(t *testing.T) {
	t.Run("ErrClosed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l := &List[int]{MaxItems: 1}
			held(t, l.Take(t.Context()))
			tk := l.Take(t.Context())

			l.Close()
			if _, err := tk.Value(); !errors.Is(err, ErrClosed) {
				t.Errorf("Value() = %v, want ErrClosed", err)
			}
		})
	})

	t.Run("canceled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l := &List[int]{MaxItems: 1}
			held(t, l.Take(t.Context()))
			ctx, cancel := context.WithCancelCause(t.Context())
			tk := l.Take(ctx)
			cancel(errStop)

			l.Close()
			if _, err := tk.Value(); err != errStop {
				t.Errorf("Value() = %v, want errStop", err)
			}
		})
	})

	t.Run("races Add", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			for range 20 {
				l := &List[int]{MaxItems: 1}
				held(t, l.Take(t.Context()))
				tk := l.Take(t.Context())

				start := make(chan struct{})
				go func() {
					<-start
					l.Add(77)
				}()
				go func() {
					<-start
					l.Close()
				}()
				close(start)
				synctest.Wait()

				v, err := tk.Value()
				if err == nil && v != 77 {
					t.Errorf("Value() = %d, nil, want 77, nil", v)
				} else if err != nil && !errors.Is(err, ErrClosed) {
					t.Errorf("Value() = %v, want nil or ErrClosed", err)
				}
				if v2, err2 := tk.Value(); v2 != v || err2 != err {
					t.Errorf("second Value() = %d, %v, want %d, %v", v2, err2, v, err)
				}
			}
		})
	})
}

func BenchmarkList(b *testing.B) {
	b.Run("uncontended", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			l := &List[int]{
				MaxItems: 10,
				New:      func() int { return 42 },
			}
			for pb.Next() {
				tk := l.Take(context.Background())
				v, err := tk.Value()
				if err != nil {
					b.Fatal("Value:", err)
				}
				if v != 42 {
					b.Fatalf("Value() = %d, want 42", v)
				}
				tk.Release()
			}
		})
	})

	b.Run("contended", func(b *testing.B) {
		b.ReportAllocs()

		var tttt atomic.Int64 // total-time-to-take
		var tttr atomic.Int64 // total-time-to-release

		l := &List[int]{
			MaxItems:   10,
			MaxWaiters: 100,
			New:        func() int { return 0 },
		}

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				ttt := time.Now()
				tk := l.Take(context.Background())
				if _, err := tk.Value(); err != nil {
					b.Fatal("Value:", err)
				}
				tttt.Add(time.Since(ttt).Nanoseconds())

				// "work"
				time.Sleep(time.Millisecond)

				ttr := time.Now()
				tk.Release()
				tttr.Add(time.Since(ttr).Nanoseconds())
			}
		})

		b.ReportMetric(float64(tttt.Load())/float64(b.N), "ns/take")
		b.ReportMetric(float64(tttr.Load())/float64(b.N), "ns/release")
	})
}

func TestListClose(t *testing.T) {
	t.Run("add before and after", func(t *testing.T) {
		var l List[int]
		for i := range 5 {
			l.Add(i)
		}
		l.Close()
		if l.Add(999) {
			t.Error("Add(999) after Close = true, want false")
		}
		for {
			tk := l.Take(done)
			v, err := tk.Value()
			if err != nil {
				break
			}
			if v < 0 || v > 4 {
				t.Errorf("Take(done) after Close = %d, want 0 through 4", v)
			}
			tk.Retire()
		}
	})

	t.Run("unblocks waiters", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l := &List[int]{MaxItems: 1, MaxWaiters: 3}
			held(t, l.Take(t.Context()))

			var inflight atomic.Int64
			for range 3 {
				inflight.Add(1)
				go func() {
					defer inflight.Add(-1)
					if _, err := l.Take(t.Context()).Value(); !errors.Is(err, ErrClosed) {
						t.Errorf("Value() = %v, want ErrClosed", err)
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
		})
	})

	t.Run("take after", func(t *testing.T) {
		var l List[int]
		l.Close()
		tk := l.Take(context.Background())
		if !tk.Ready() {
			t.Error("Ready() = false, want true")
		}
		if _, err := tk.Value(); !errors.Is(err, ErrClosed) {
			t.Errorf("Value() = %v, want ErrClosed", err)
		}
	})

	t.Run("drain ready items", func(t *testing.T) {
		var l List[int]
		for i := range 5 {
			l.Add(i)
		}
		l.Close()

		seen := make(map[int]bool)
		for i := range 5 {
			v, err := l.Take(context.Background()).Value()
			if err != nil {
				t.Fatalf("Take %d after Close: Value() = %v, want nil", i, err)
			}
			seen[v] = true
		}
		if len(seen) != 5 {
			t.Errorf("drained %v, want 5 distinct items", seen)
		}
		if _, err := l.Take(context.Background()).Value(); !errors.Is(err, ErrClosed) {
			t.Errorf("Value() after draining = %v, want ErrClosed", err)
		}
	})

	t.Run("release after", func(t *testing.T) {
		var l List[int]
		l.Add(5)
		tk := held(t, l.Take(context.Background()))
		l.Close()

		// A released item stays drainable, so its owner can dispose of it.
		tk.Release()
		if v, err := l.Take(done).Value(); v != 5 || err != nil {
			t.Errorf("Take(done).Value() = %d, %v, want the released item 5, nil", v, err)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		var l List[int]
		l.Close()
		l.Close()
		l.Close()
		if _, err := l.Take(context.Background()).Value(); !errors.Is(err, ErrClosed) {
			t.Errorf("Value() = %v, want ErrClosed", err)
		}
	})
}

func TestListRetire(t *testing.T) {
	t.Run("frees capacity", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var loads atomic.Int64
			l := &List[int]{
				MaxItems: 1,
				New:      func() int { return int(loads.Add(1)) },
			}
			tk := held(t, l.Take(t.Context()))
			if v, _ := tk.Value(); v != 1 {
				t.Fatalf("first Value() = %d, want 1", v)
			}
			tk.Retire()
			if v, err := l.Take(t.Context()).Value(); v != 2 || err != nil {
				t.Fatalf("replacement Value() = %d, %v, want 2, nil", v, err)
			}
		})
	})

	t.Run("starts one load", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l := &List[int]{MaxItems: 1}
			defer func() {
				l.Close()
				synctest.Wait()
			}()
			tk := held(t, l.Take(t.Context()))

			var started atomic.Int64
			l.New = func() int {
				started.Add(1)
				return 42
			}

			// Three Tickets wait. If Retire started more than one load,
			// the second and third would be admitted too.
			first := l.Take(t.Context())
			l.Take(t.Context())
			l.Take(t.Context())
			synctest.Wait()
			if got := started.Load(); got != 0 {
				t.Fatalf("loads started before Retire = %d, want 0", got)
			}

			tk.Retire()
			synctest.Wait()
			if got := started.Load(); got != 1 {
				t.Fatalf("loads started after Retire = %d, want 1", got)
			}
			if v, err := first.Value(); v != 42 || err != nil {
				t.Fatalf("first.Value() = %d, %v, want 42, nil", v, err)
			}
		})
	})

	t.Run("serves in order", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var next atomic.Int64
			l := &List[int]{
				MaxItems:   1,
				MaxWaiters: 3,
				New:        func() int { return int(next.Add(1)) },
			}
			tk := held(t, l.Take(t.Context()))

			var waiting [3]Ticket[int]
			for i := range waiting {
				waiting[i] = l.Take(t.Context())
			}

			// Each waiter retires its item as soon as it has it, so the
			// List creates a replacement for the next one in line.
			var got [3]int
			for i := range waiting {
				go func() {
					v, err := waiting[i].Value()
					if err != nil {
						t.Errorf("waiting[%d].Value() = %v", i, err)
						return
					}
					got[i] = v
					waiting[i].Retire()
				}()
			}
			tk.Retire()
			synctest.Wait()
			if want := [3]int{2, 3, 4}; got != want {
				t.Fatalf("got = %v, want %v", got, want)
			}
		})
	})

	t.Run("does not over-create", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var started atomic.Int64
			l := &List[int]{
				MaxItems:   1,
				MaxWaiters: 3,
				New:        func() int { return int(started.Add(1)) },
			}
			var tks [3]Ticket[int]
			for i := range tks {
				tks[i] = l.Take(t.Context())
			}
			synctest.Wait()
			if !tks[0].Ready() || tks[1].Ready() || tks[2].Ready() {
				t.Fatalf("Ready() = %v, %v, %v, want true, false, false",
					tks[0].Ready(), tks[1].Ready(), tks[2].Ready())
			}

			tks[0].Retire()
			synctest.Wait()
			if v, _ := tks[1].Value(); v != 2 {
				t.Fatalf("tks[1].Value() = %d, want 2", v)
			}
			if tks[2].Ready() {
				t.Fatal("tks[2] admitted without a new item")
			}

			l.Add(42)
			if v, _ := tks[2].Value(); v != 42 {
				t.Fatalf("tks[2].Value() = %d, want 42", v)
			}
		})
	})

	t.Run("after Close", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l := &List[int]{MaxItems: 1}
			tk := held(t, l.Take(t.Context()))
			l.New = func() int { panic("should not create an item after Close") }
			l.Close()
			tk.Retire()
			if _, err := l.Take(t.Context()).Value(); !errors.Is(err, ErrClosed) {
				t.Fatalf("Value() = %v, want ErrClosed", err)
			}
		})
	})

	t.Run("replacement skips canceled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var next atomic.Int64
			l := &List[int]{
				MaxItems:   1,
				MaxWaiters: 2,
				New:        func() int { return int(next.Add(1)) },
			}
			tk := held(t, l.Take(t.Context()))

			ctx, cancel := context.WithCancel(t.Context())
			oldest := l.Take(ctx)
			newer := l.Take(t.Context())
			cancel()

			tk.Retire()
			if v, err := newer.Value(); v != 2 || err != nil {
				t.Errorf("newer.Value() = %d, %v, want 2, nil", v, err)
			}
			if _, err := oldest.Value(); !errors.Is(err, context.Canceled) {
				t.Errorf("oldest.Value() = %v, want context.Canceled", err)
			}
		})
	})
}
