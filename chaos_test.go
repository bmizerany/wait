package wait

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"testing"
)

func TestListChaos(t *testing.T) {
	// Goroutines take items every way there is, cancel before, during and
	// after Value, release twice and through copies, and in some rounds
	// one of them closes the List midway. Every item must end up kept or
	// drained exactly once.
	const workers, ops = 8, 300
	for seed := range uint64(30) {
		var l List[int]
		for v := range 2 * workers {
			l.Add(v)
		}
		kept := make([][]int, workers)
		var wg sync.WaitGroup
		for i := range workers {
			r := rand.New(rand.NewPCG(seed, uint64(i)))
			wg.Go(func() {
				for n := range ops {
					if seed%3 == 0 && i == 0 && n == ops/2 {
						l.Close()
					}
					if v, ok := chaos(t, &l, r, len(kept[i]) == 0); ok {
						kept[i] = append(kept[i], v)
					}
				}
			})
		}
		wg.Wait()
		l.Close()

		count := make(map[int]int)
		for _, vs := range kept {
			for _, v := range vs {
				count[v]++
			}
		}
		for {
			tk, ok := l.TryTake()
			if !ok {
				break
			}
			v, _ := tk.Value()
			count[v]++
		}
		for v := range 2 * workers {
			if count[v] != 1 {
				t.Errorf("seed %d: item %d found %d times, want 1", seed, v, count[v])
			}
		}
		if n := l.waiters.q.Len(); n != 0 {
			t.Errorf("seed %d: %d waiters left in line after Close", seed, n)
		}
	}
}

// chaos takes an item from l one random way and checks what Value says.
// If mayKeep, chaos sometimes keeps the item, never releasing its Ticket,
// and returns it and true. Otherwise it releases the Ticket.
//
// With at most one item kept per goroutine and two items per goroutine in
// the List, a Take whose ctx is never canceled is always served.
func chaos(t *testing.T, l *List[int], r *rand.Rand, mayKeep bool) (int, bool) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	var tk Ticket[int]
	switch r.IntN(5) {
	case 0:
		tk, _ = l.TryTake()
	case 1:
		cancel(errStop)
		tk = l.Take(ctx)
	case 2:
		tk = l.Take(ctx)
		go cancel(errStop) // races admission and Value
	default:
		tk = l.Take(ctx)
	}
	if r.IntN(4) == 0 {
		tk.Release() // before Value: leaves the line or gives the item back
	}

	doneBefore := ctx.Err() != nil
	v, err := tk.Value()
	switch {
	case err == nil && doneBefore:
		t.Errorf("Value() = %d, nil for a ctx done before Value", v)
	case err != nil && !errors.Is(err, errStop) && !errors.Is(err, ErrClosed) && !errors.Is(err, ErrReleased):
		t.Errorf("Value() = %v", err)
	}
	if err == nil && r.IntN(2) == 0 {
		cancel(errStop) // too late: Value has handed the item out
		if v2, err := tk.Value(); v2 != v || err != nil {
			t.Errorf("Value() after cancel = %d, %v, want %d, nil", v2, err, v)
		}
	}
	if err == nil && mayKeep && r.IntN(8) == 0 {
		return v, true
	}

	cp := tk
	cp.Release()
	tk.Release() // the copy's Release ended tk too
	return 0, false
}
