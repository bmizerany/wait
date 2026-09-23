package wait

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

func TestTicketReleaseTwice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, free := testGate(2)

		tk, ok := g.TryTake(2)
		if !ok {
			t.Fatal("TryTake(2) = false, want true")
		}
		tk.Release()
		tk.Release()
		if got := free(); got != 2 {
			t.Fatalf("free after TryTake release twice = %d, want 2", got)
		}

		tk = held(t, g.Take(t.Context(), 2))
		waiter := g.Take(t.Context(), 2)
		tk.Release() // admits waiter
		tk.Release() // must not credit capacity waiter now holds
		if !waiter.Ready() {
			t.Fatal("waiter not admitted after Release")
		}
		if got := free(); got != 0 {
			t.Fatalf("free after Take release twice = %d, want 0", got)
		}
	})
}

func TestTicketReleaseTwiceList(t *testing.T) {
	var l List[int]
	l.Add(1)
	tk := held(t, l.Take(context.Background()))
	tk.Release()
	tk.Release() // must not store item 1 twice

	held(t, l.Take(context.Background()))
	if _, ok := l.TryTake(); ok {
		t.Fatal("TryTake() = true, want false (item 1 stored twice)")
	}
}

func TestTicketStale(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, free := testGate(1)

		old := held(t, g.Take(t.Context(), 1))
		old.Release()
		cur := held(t, g.Take(t.Context(), 1)) // usually reuses old's waiter
		dup := cur

		old.Release() // must not release cur
		if got := free(); got != 0 {
			t.Fatalf("free after stale Release = %d, want 0", got)
		}
		if _, err := old.Value(); !errors.Is(err, ErrReleased) {
			t.Fatalf("stale Value() = %v, want ErrReleased", err)
		}
		dup.Release()
		cur.Release() // dup already released it
		if got := free(); got != 1 {
			t.Fatalf("free after releasing cur and dup = %d, want 1", got)
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
	tk.Retire()
}

func TestTicketFailed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, free := testGate(1)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		tk := g.Take(ctx, 1)
		if !tk.Ready() {
			t.Error("Ready() = false, want true")
		}
		tk.Release()
		if _, err := tk.Value(); !errors.Is(err, context.Canceled) {
			t.Fatalf("Value() = %v, want context.Canceled", err)
		}
		if got := free(); got != 1 {
			t.Fatalf("free after releasing a failed Ticket = %d, want 1", got)
		}
	})
}

func TestTicketReleaseWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, free := testGate(2)
		holder := held(t, g.Take(t.Context(), 2))

		big := g.Take(t.Context(), 2)
		small := g.Take(t.Context(), 1)
		big.Release() // leaves the line; small is at its head

		holder.Release()
		if !small.Ready() {
			t.Fatal("small not admitted after big left the line")
		}
		if _, err := big.Value(); !errors.Is(err, ErrReleased) {
			t.Fatalf("big.Value() = %v, want ErrReleased", err)
		}
		if got := free(); got != 1 {
			t.Fatalf("free = %d, want 1", got)
		}
	})
}

func TestTicketRetireGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, free := testGate(1)
		tk := held(t, g.Take(t.Context(), 1))
		tk.Retire()
		tk.Release() // no effect after Retire
		if got := free(); got != 0 {
			t.Fatalf("free after Retire = %d, want 0 (capacity stays spent)", got)
		}
	})
}

func TestTicketAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("sync.Pool drops items at random under the race detector")
	}
	ctx := context.Background()
	value := func(t *testing.T, tk Ticket[int]) {
		if _, err := tk.Value(); err != nil {
			t.Fatal("Value:", err)
		}
	}

	g, _ := testGate(1)
	// At its limit, so a queued Take does not start creating an item.
	l := &List[int]{MaxItems: 1}
	held(t, l.Take(ctx)).Release()
	checkAllocs(t, "Gate Take", func() {
		tk := g.Take(ctx, 1)
		value(t, tk)
		tk.Release()
	})
	checkAllocs(t, "List Take", func() {
		tk := l.Take(ctx)
		value(t, tk)
		tk.Release()
	})

	// Queued: the next Ticket joins the line, and releasing the held one
	// admits it.
	gheld := held(t, g.Take(ctx, 1))
	checkAllocs(t, "Gate queued Take", func() {
		next := g.Take(ctx, 1)
		gheld.Release()
		value(t, next)
		gheld = next
	})
	lheld := held(t, l.Take(ctx))
	checkAllocs(t, "List queued Take", func() {
		next := l.Take(ctx)
		lheld.Release()
		value(t, next)
		lheld = next
	})
}

func checkAllocs(t *testing.T, name string, f func()) {
	t.Helper()
	if n := testing.AllocsPerRun(100, f); n != 0 {
		t.Errorf("%s allocates %v times per run, want 0", name, n)
	}
}
