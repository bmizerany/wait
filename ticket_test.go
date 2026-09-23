package wait

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

// done is already done, so Take with it takes only what is available at once.
var done = func() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}()

func TestTicketReleaseTwice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g, free := testGate(2)

		tk := g.Take(done, 2)
		if _, err := tk.Value(); err != nil {
			t.Fatal("Take(done, 2) not admitted, want admitted")
		}
		tk.Release()
		tk.Release()
		if got := free(); got != 2 {
			t.Fatalf("free after Take(done) release twice = %d, want 2", got)
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
	if _, err := l.Take(done).Value(); err == nil {
		t.Fatal("Take(done) admitted, want false (item 1 stored twice)")
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

		tk := g.Take(done, 2) // does not fit, so it fails at once
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
		releases := 0
		g := &Gate[int]{Release: func(int) { releases++ }}
		tk := held(t, g.Take(t.Context(), 1))
		tk.Retire()
		tk.Release() // no effect after Retire
		if releases != 0 {
			t.Fatalf("Release calls after Retire then Release = %d, want 0 (capacity stays spent)", releases)
		}
	})
}

func TestTicketRetireList(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var loads atomic.Int64
		l := &List[int]{
			MaxItems: 1,
			New:      func() int { return int(loads.Add(1)) },
		}
		tk := held(t, l.Take(t.Context())) // item 1
		waiter := l.Take(t.Context())

		tk.Retire() // drops item 1, freeing its place for a new item
		if v, err := waiter.Value(); v != 2 || err != nil {
			t.Fatalf("waiter.Value() = %d, %v, want 2, nil", v, err)
		}
		tk.Release() // must not bring item 1 back
		if _, err := l.Take(done).Value(); err == nil {
			t.Fatal("Take(done) admitted after Retire then Release, want false")
		}
		if got := loads.Load(); got != 2 {
			t.Fatalf("New calls = %d, want 2", got)
		}
	})
}

func TestTicketReleaseThenRetire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var loads atomic.Int64
		l := &List[int]{
			MaxItems: 1,
			New:      func() int { return int(loads.Add(1)) },
		}
		tk := held(t, l.Take(t.Context()))
		tk.Release()
		tk.Retire() // must not drop item 1 or free its place

		if v, err := l.Take(done).Value(); v != 1 || err != nil {
			t.Fatalf("Take(done).Value() = %d, %v, want item 1 back after Release", v, err)
		}
		next := l.Take(t.Context())
		synctest.Wait() // let any wrongly started New run
		if next.Ready() {
			t.Fatal("second Take admitted: Retire freed a place Release had kept")
		}
		if _, err := tk.Value(); !errors.Is(err, ErrReleased) {
			t.Fatalf("Value() after Release and Retire = %v, want ErrReleased", err)
		}

		g, free := testGate(1)
		gt := held(t, g.Take(t.Context(), 1))
		gt.Release()
		gt.Retire()
		if got := free(); got != 1 {
			t.Fatalf("Gate free after Release then Retire = %d, want 1", got)
		}
	})
}

func TestTicketRetireWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &List[int]{MaxItems: 1, MaxWaiters: 2}
		held(t, l.Take(t.Context()))

		tk := l.Take(t.Context())
		tk.Retire() // leaves the line, freeing its MaxWaiters place
		tk.Release()
		if _, err := tk.Value(); !errors.Is(err, ErrReleased) {
			t.Fatalf("Value() after Retire = %v, want ErrReleased", err)
		}

		// Both places are free, and the two new Tickets are distinct.
		a := l.Take(t.Context())
		b := l.Take(t.Context())
		l.Add(10)
		l.Add(20)
		if v, err := a.Value(); v != 10 || err != nil {
			t.Fatalf("a.Value() = %d, %v, want 10, nil", v, err)
		}
		if v, err := b.Value(); v != 20 || err != nil {
			t.Fatalf("b.Value() = %d, %v, want 20, nil", v, err)
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
