package wait

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

// done is already done, so Take with it takes only what is available at once.
var done = func() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}()

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
		if _, err := l.Take(done).Value(); err == nil {
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
		if _, err := l.Take(done).Value(); err == nil {
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
		if _, err := l.Take(done).Value(); err == nil {
			t.Fatal("stale Release gave back cur's item")
		}
		if _, err := old.Value(); !errors.Is(err, ErrReleased) {
			t.Fatalf("stale Value() = %v, want ErrReleased", err)
		}
		dup.Release()
		cur.Release() // dup already released it
		held(t, l.Take(done))
		if _, err := l.Take(done).Value(); err == nil {
			t.Fatal("releasing cur and dup stored item 1 twice")
		}
	})
}

func TestTicketZero(t *testing.T) {
	var tk Ticket[int]
	if !tk.Ready() {
		t.Error("Ready() = false, want true")
	}
	if _, err := tk.Value(); !errors.Is(err, ErrReleased) {
		t.Errorf("Value() = %v, want ErrReleased", err)
	}
	tk.Release()
}

func TestTicketFailed(t *testing.T) {
	var l List[int]
	tk := l.Take(done) // nothing ready, so it fails at once
	if !tk.Ready() {
		t.Error("Ready() = false, want true")
	}
	tk.Release()
	if _, err := tk.Value(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Value() = %v, want context.Canceled", err)
	}
	l.Add(1)
	if v, err := l.Take(done).Value(); v != 1 || err != nil {
		t.Fatalf("Take(done).Value() = %d, %v, want 1, nil", v, err)
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
}

func checkAllocs(t *testing.T, name string, f func()) {
	t.Helper()
	if n := testing.AllocsPerRun(100, f); n != 0 {
		t.Errorf("%s allocates %v times per run, want 0", name, n)
	}
}
