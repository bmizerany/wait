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
		l := &List[int]{MaxWaiters: 3}
		l.Add(0)
		l.Add(1)

		tk := held(t, l.Take(t.Context())) // item 1, the warmest
		tk.Release()                       // item 1 is on top again

		a := held(t, l.Take(t.Context()))
		b := held(t, l.Take(t.Context()))
		if v, _ := a.Value(); v != 1 {
			t.Errorf("a.Value() = %d, want 1", v)
		}
		if v, _ := b.Value(); v != 0 {
			t.Errorf("b.Value() = %d, want 0", v)
		}

		// Both items are out: three Tickets wait, and a fourth is
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
		for i, want := range []int{1, 0, 2} {
			if v, err := waiting[i].Value(); v != want || err != nil {
				t.Errorf("waiting[%d].Value() = %d, %v, want %d, nil", i, v, err, want)
			}
		}

		l.Close()
		if l.Add(42) {
			t.Error("Add after Close = true, want false")
		}
	})
}

func TestListTakeCancel(t *testing.T) {
	t.Run("early cancel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var l List[int]
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
		var l List[int]
		l.Add(41)
		l.Add(42)
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(errStop)

		// The canceled context wins, and the warmest item stays on top
		// for the next caller.
		if _, err := l.Take(ctx).Value(); err != errStop {
			t.Errorf("Value() = %v, want errStop", err)
		}
		if v, err := tryTake(&l).Value(); v != 42 || err != nil {
			t.Errorf("tryTake().Value() = %d, %v, want 42, nil", v, err)
		}
	})

	t.Run("waiting cancel", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l := new(List[int]) // creates nothing, so Take waits

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
		l := new(List[int])

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
		l := &List[int]{MaxWaiters: 1}

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
		var l List[int]
		l.Add(42)
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
		if v, err := tryTake(&l).Value(); v != 42 || err != nil {
			t.Errorf("tryTake().Value() = %d, %v, want the released item 42, nil", v, err)
		}
	})
}

func TestListCancelWins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l List[int]
		l.Add(44)
		holder := held(t, l.Take(t.Context()))

		ctx, cancel := context.WithCancelCause(t.Context())
		tk := l.Take(ctx)
		next := l.Take(t.Context())
		holder.Release() // admits tk
		cancel(errStop)
		if v, err := tk.Value(); err != errStop {
			t.Errorf("Value() = %d, %v, want errStop", v, err)
		}
		if v, err := next.Value(); v != 44 || err != nil {
			t.Fatalf("next.Value() = %d, %v, want the item tk gave back, 44, nil", v, err)
		}

		tk.Release() // must not give 44 back again
		next.Release()
		held(t, tryTake(&l))
		if _, err := tryTake(&l).Value(); err == nil {
			t.Fatal("item 44 was given back twice")
		}
	})
}

func TestListCancelWinsReady(t *testing.T) {
	var l List[int]
	l.Add(45)
	l.Add(46)
	ctx, cancel := context.WithCancelCause(t.Context())
	tk := l.Take(ctx) // holds 46 already
	cancel(errStop)
	if v, err := tk.Value(); err != errStop {
		t.Errorf("Value() = %d, %v, want errStop", v, err)
	}
	if v, err := tryTake(&l).Value(); v != 46 || err != nil {
		t.Errorf("tryTake().Value() = %d, %v, want 46 back on top, nil", v, err)
	}
}

func TestListCancelAfterValue(t *testing.T) {
	var l List[int]
	l.Add(47)
	ctx, cancel := context.WithCancel(t.Context())
	tk := l.Take(ctx)
	if v, err := tk.Value(); v != 47 || err != nil {
		t.Fatalf("Value() = %d, %v, want 47, nil", v, err)
	}

	// Value has handed 47 out, so it stays out.
	cancel()
	if v, err := tk.Value(); v != 47 || err != nil {
		t.Errorf("Value() after cancel = %d, %v, want 47, nil", v, err)
	}
	if _, err := tryTake(&l).Value(); err == nil {
		t.Fatal("cancel gave back 47 while tk holds it")
	}
	tk.Release()
	if v, err := tryTake(&l).Value(); v != 47 || err != nil {
		t.Errorf("tryTake().Value() after Release = %d, %v, want 47, nil", v, err)
	}
}

func TestListCancelWinsWaiting(t *testing.T) {
	// Admission and cancellation both reach a Ticket blocked in Value
	// before Value runs again. Whichever wakes it, cancellation wins.
	for _, admitFirst := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			var l List[int]
			l.Add(48)
			holder := held(t, l.Take(t.Context()))

			ctx, cancel := context.WithCancel(t.Context())
			tk := l.Take(ctx)
			go func() {
				if v, err := tk.Value(); !errors.Is(err, context.Canceled) {
					t.Errorf("admitFirst=%v: Value() = %d, %v, want context.Canceled", admitFirst, v, err)
				}
			}()
			synctest.Wait()

			if admitFirst {
				holder.Release() // admits tk and wakes Value
				cancel()
			} else {
				cancel()         // wakes Value
				holder.Release() // skips tk, whose ctx is done
			}
			synctest.Wait()
			if v, err := tryTake(&l).Value(); v != 48 || err != nil {
				t.Errorf("admitFirst=%v: tryTake().Value() = %d, %v, want 48, nil", admitFirst, v, err)
			}
		})
	}
}

func TestTryTake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &List[int]{MaxWaiters: 1}
		if _, ok := l.TryTake(); ok {
			t.Fatal("TryTake() on an empty List = true, want false")
		}
		l.Add(1)
		l.Add(2)
		tk, ok := l.TryTake()
		if v, err := tk.Value(); !ok || v != 2 || err != nil {
			t.Fatalf("TryTake() = %d, %v, %v, want the warmest item 2, nil, true", v, err, ok)
		}
		one := held(t, l.Take(t.Context()))

		// Nothing is ready and the line is full. TryTake reports false
		// without joining the line, so it neither fails with
		// ErrMaxWaiters nor turns away the next caller.
		ahead := l.Take(t.Context())
		if _, ok := l.TryTake(); ok {
			t.Fatal("TryTake() with nothing ready = true, want false")
		}
		if n := l.waiters.q.Len(); n != 1 {
			t.Errorf("line holds %d waiters after TryTake, want 1", n)
		}
		tk.Release() // admits ahead
		if v, err := ahead.Value(); v != 2 || err != nil {
			t.Errorf("ahead.Value() = %d, %v, want 2, nil", v, err)
		}

		one.Release()
		l.Close()
		tk, ok = l.TryTake()
		if v, err := tk.Value(); !ok || v != 1 || err != nil {
			t.Errorf("TryTake() after Close = %d, %v, %v, want 1, nil, true", v, err, ok)
		}
		if _, ok := l.TryTake(); ok {
			t.Error("TryTake() after draining = true, want false")
		}
	})
}

func TestTicketReleaseAdmitted(t *testing.T) {
	// A Ticket released after admission but before Value gives its item
	// back: to the Ticket behind it, else to the ready items.
	synctest.Test(t, func(t *testing.T) {
		var l List[int]
		tk := l.Take(t.Context())
		behind := l.Take(t.Context())
		l.Add(3) // admits tk
		tk.Release()
		if v, err := behind.Value(); v != 3 || err != nil {
			t.Errorf("behind.Value() = %d, %v, want 3, nil", v, err)
		}
		tk = l.Take(t.Context())
		l.Add(4) // admits tk
		tk.Release()
		if v, err := tryTake(&l).Value(); v != 4 || err != nil {
			t.Errorf("tryTake().Value() = %d, %v, want 4, nil", v, err)
		}
	})
}

func TestListTakeDoneReady(t *testing.T) {
	var l List[int]
	l.Add(1)
	tk := l.Take(done)
	tk.Release() // nothing to release: the item never left the List
	if _, err := tk.Value(); !errors.Is(err, context.Canceled) {
		t.Errorf("Value() after Release = %v, want context.Canceled", err)
	}
	if v, err := tryTake(&l).Value(); v != 1 || err != nil {
		t.Errorf("tryTake().Value() = %d, %v, want 1, nil", v, err)
	}
}

func TestListCloseWaiting(t *testing.T) {
	t.Run("ErrClosed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l := new(List[int])
			tk := l.Take(t.Context())

			l.Close()
			if _, err := tk.Value(); !errors.Is(err, ErrClosed) {
				t.Errorf("Value() = %v, want ErrClosed", err)
			}
		})
	})

	t.Run("canceled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l := new(List[int])
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
				l := new(List[int])
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
	b.Run("ready", func(b *testing.B) {
		var l List[int]
		l.Add(42)
		for b.Loop() {
			tk := l.Take(b.Context())
			if _, err := tk.Value(); err != nil {
				b.Fatal("Value:", err)
			}
			tk.Release()
		}
	})

	// Each Ticket joins the line, and releasing the held one admits it.
	b.Run("queued", func(b *testing.B) {
		var l List[int]
		l.Add(42)
		holder := l.Take(b.Context())
		for b.Loop() {
			next := l.Take(b.Context())
			holder.Release()
			if _, err := next.Value(); err != nil {
				b.Fatal("Value:", err)
			}
			holder = next
		}
	})

	b.Run("uncontended", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			var l List[int]
			for range 10 {
				l.Add(42)
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

		l := &List[int]{MaxWaiters: 100}
		for range 10 {
			l.Add(0)
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
			tk := l.Take(context.Background()) // never waits after Close
			v, err := tk.Value()
			if err != nil {
				break
			}
			if v < 0 || v > 4 {
				t.Errorf("Take after Close = %d, want 0 through 4", v)
			}
			// Not released: drained items are ours to dispose of.
		}
	})

	t.Run("unblocks waiters", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			l := &List[int]{MaxWaiters: 3}

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

	t.Run("take after with done ctx", func(t *testing.T) {
		var l List[int]
		l.Add(6)
		l.Close()

		// The done ctx wins over both the ready item and ErrClosed.
		if _, err := l.Take(done).Value(); !errors.Is(err, context.Canceled) {
			t.Errorf("Take(done).Value() = %v, want context.Canceled", err)
		}
		if v, err := l.Take(context.Background()).Value(); v != 6 || err != nil {
			t.Errorf("Value() = %d, %v, want 6, nil", v, err)
		}
		if _, err := l.Take(done).Value(); !errors.Is(err, context.Canceled) {
			t.Errorf("Take(done).Value() after draining = %v, want context.Canceled", err)
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
		if v, err := l.Take(context.Background()).Value(); v != 5 || err != nil {
			t.Errorf("Value() = %d, %v, want the released item 5, nil", v, err)
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
