package wait

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

// done is already done, so Take with it always fails.
var done = func() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}()

// tryTake returns l.TryTake's Ticket, which is the zero Ticket, whose
// Value reports ErrReleased, if no item is ready.
func tryTake[T any](l *List[T]) Ticket[T] {
	tk, _ := l.TryTake()
	return tk
}

// held waits for tk to be admitted, failing t if it is not, and returns tk.
func held[T any](t *testing.T, tk Ticket[T]) Ticket[T] {
	t.Helper()
	if _, err := tk.Value(); err != nil {
		t.Fatalf("Value() = %v, want admission", err)
	}
	return tk
}

func TestTicketReleaseTwice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l List[int]
		l.Add(1)

		tk := held(t, l.Take(t.Context()))
		tk.Release()
		tk.Release() // must not store item 1 twice
		held(t, l.Take(t.Context()))
		if _, err := tryTake(&l).Value(); err == nil {
			t.Fatal("item 1 was stored twice")
		}
	})
}

func TestTicketReleaseTwiceWaiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l List[int]
		l.Add(1)

		tk := held(t, l.Take(t.Context()))
		waiter := l.Take(t.Context())
		tk.Release() // hands item 1 to waiter
		tk.Release() // must not hand it out again
		if v, err := waiter.Value(); v != 1 || err != nil {
			t.Fatalf("waiter.Value() = %d, %v, want 1, nil", v, err)
		}
		if _, err := tryTake(&l).Value(); err == nil {
			t.Fatal("item 1 was stored while waiter holds it")
		}
	})
}

func TestTicketStale(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l List[int]
		l.Add(1)

		old := held(t, l.Take(t.Context()))
		old.Release()
		cur := held(t, l.Take(t.Context())) // usually reuses old's waiter
		dup := cur

		old.Release() // must not release cur
		if _, err := tryTake(&l).Value(); err == nil {
			t.Fatal("stale Release gave back cur's item")
		}
		if _, err := old.Value(); !errors.Is(err, ErrReleased) {
			t.Fatalf("stale Value() = %v, want ErrReleased", err)
		}
		dup.Release()
		cur.Release() // dup already released it
		held(t, tryTake(&l))
		if _, err := tryTake(&l).Value(); err == nil {
			t.Fatal("releasing cur and dup stored item 1 twice")
		}
	})
}

func TestTicketZero(t *testing.T) {
	var tk Ticket[int]
	if _, err := tk.Value(); !errors.Is(err, ErrReleased) {
		t.Errorf("Value() = %v, want ErrReleased", err)
	}
	tk.Release()
}

func TestTicketFailed(t *testing.T) {
	var l List[int]
	tk := l.Take(done) // fails at once
	tk.Release()
	if _, err := tk.Value(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Value() = %v, want context.Canceled", err)
	}
	l.Add(1)
	if v, err := tryTake(&l).Value(); v != 1 || err != nil {
		t.Fatalf("tryTake().Value() = %d, %v, want 1, nil", v, err)
	}
}

func TestTicketReleaseWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l List[int]
		first := l.Take(t.Context())
		second := l.Take(t.Context())
		first.Release() // leaves the line; second is at its front

		l.Add(1)
		if v, err := second.Value(); v != 1 || err != nil {
			t.Fatalf("second.Value() = %d, %v, want 1, nil", v, err)
		}
		if _, err := first.Value(); !errors.Is(err, ErrReleased) {
			t.Fatalf("first.Value() = %v, want ErrReleased", err)
		}
	})
}

func TestTicketLeave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var l List[int]

		// Waiting: Leave takes it out of the line, and Value doesn't wait.
		tk := l.Take(t.Context())
		if !tk.Leave() {
			t.Fatal("Leave() of a waiting Ticket = false, want true")
		}
		if _, err := tk.Value(); !errors.Is(err, ErrReleased) {
			t.Errorf("Value() after Leave = %v, want ErrReleased", err)
		}
		if cp := tk; cp.Leave() {
			t.Error("second Leave() on a copy = true, want false")
		}
		l.Add(1) // goes to the ready items, not to tk
		tk.Release()
		if v, err := tryTake(&l).Value(); v != 1 || err != nil {
			t.Fatalf("tryTake().Value() = %d, %v, want 1, nil", v, err)
		}

		// Admitted, unread: Leave keeps the item for Value, and Release
		// doesn't give it back.
		l.Add(2)
		tk = l.Take(t.Context())
		if !tk.Leave() {
			t.Fatal("Leave() of an admitted Ticket = false, want true")
		}
		if v, err := tk.Value(); v != 2 || err != nil {
			t.Errorf("Value() after Leave = %d, %v, want 2, nil", v, err)
		}
		tk.Release()
		if _, ok := l.TryTake(); ok {
			t.Error("Release after Leave gave the item back")
		}
		l.Add(5)
		held(t, l.Take(t.Context())).Release() // reuses tk's waiter, which must not stay left
		if v, err := tryTake(&l).Value(); v != 5 || err != nil {
			t.Errorf("tryTake().Value() = %d, %v, want 5 given back, nil", v, err)
		}

		// Taken: the same, after Value.
		l.Add(3)
		tk = held(t, l.Take(t.Context()))
		if !tk.Leave() {
			t.Fatal("Leave() after Value = false, want true")
		}
		tk.Release()
		if _, ok := l.TryTake(); ok {
			t.Error("Release after Value and Leave gave the item back")
		}

		// Failed in line.
		ctx, cancel := context.WithCancelCause(t.Context())
		tk = l.Take(ctx)
		cancel(errStop)
		if !tk.Leave() {
			t.Error("Leave() of a failed Ticket = false, want true")
		}
		if _, err := tk.Value(); err != errStop {
			t.Errorf("Value() = %v, want errStop", err)
		}
		tk.Release()

		// Released, failed in Take, and zero: nothing to leave.
		if tk.Leave() {
			t.Error("Leave() after Release = true, want false")
		}
		if l.Take(done).Leave() {
			t.Error("Leave() of a Ticket that failed in Take = true, want false")
		}
		if (Ticket[int]{}).Leave() {
			t.Error("Leave() of the zero Ticket = true, want false")
		}
	})
}

func TestTicketLeaveCanceled(t *testing.T) {
	// Leave doesn't change the cancellation rule: an item Value hasn't
	// returned goes back if ctx is done first.
	var l List[int]
	l.Add(4)
	ctx, cancel := context.WithCancelCause(t.Context())
	tk := l.Take(ctx)
	tk.Leave()
	cancel(errStop)
	if _, err := tk.Value(); err != errStop {
		t.Errorf("Value() = %v, want errStop", err)
	}
	if v, err := tryTake(&l).Value(); v != 4 || err != nil {
		t.Errorf("tryTake().Value() = %d, %v, want 4 back, nil", v, err)
	}
}

func TestTicketAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("sync.Pool drops items at random under the race detector")
	}
	ctx := context.Background()
	var l List[int]
	l.Add(1)
	checkAllocs(t, "Take", func() {
		tk := l.Take(ctx)
		if _, err := tk.Value(); err != nil {
			t.Fatal("Value:", err)
		}
		tk.Release()
	})

	// Queued: the next Ticket joins the line, and releasing the held one
	// admits it.
	holder := held(t, l.Take(ctx))
	checkAllocs(t, "queued Take", func() {
		next := l.Take(ctx)
		holder.Release()
		if _, err := next.Value(); err != nil {
			t.Fatal("Value:", err)
		}
		holder = next
	})
	holder.Release()

	// Leave, replace, and Release: the Ticket is reused all the same.
	checkAllocs(t, "Leave", func() {
		tk := l.Take(ctx)
		v, err := tk.Value()
		if err != nil {
			t.Fatal("Value:", err)
		}
		if tk.Leave() {
			l.Add(v)
		}
		tk.Release()
	})
}

func checkAllocs(t *testing.T, name string, f func()) {
	t.Helper()
	if n := testing.AllocsPerRun(100, f); n != 0 {
		t.Errorf("%s allocates %v times per run, want 0", name, n)
	}
}
