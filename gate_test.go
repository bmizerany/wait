package wait_test

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
)

type demand struct {
	name string
	n    int
}

func TestGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var order []string // appended only by the turn holder
		acquire, release := gate(4,
			func(free int, d demand) (int, bool) {
				if d.n > free {
					return free, false
				}
				order = append(order, d.name)
				return free - d.n, true
			},
			func(free int, d demand) int { return free + d.n })
		ctx := t.Context()

		acquire(ctx, demand{"A", 3})
		wait := func(ctx context.Context, d demand) <-chan error {
			ch := make(chan error, 1)
			go func() { ch <- acquire(ctx, d) }()
			synctest.Wait()
			return ch
		}

		b := wait(ctx, demand{"B", 2}) // 1 free: B waits at the front
		c := wait(ctx, demand{"C", 1}) // 1 would fit, but C stays behind B
		if want := "[A]"; fmt.Sprint(order) != want {
			t.Fatalf("order = %v, want %s", order, want)
		}

		release(demand{"A", 3}) // 4 free: B, then C
		<-b
		<-c
		if want := "[A B C]"; fmt.Sprint(order) != want {
			t.Fatalf("order = %v, want %s", order, want)
		}

		// D waits for 2 and gives up; E, behind it, gets in once 1 is back.
		cctx, cancel := context.WithCancel(ctx)
		d := wait(cctx, demand{"D", 2})
		e := wait(ctx, demand{"E", 1})
		cancel()
		if err := <-d; err != context.Canceled {
			t.Fatalf("D = %v, want context.Canceled", err)
		}
		release(demand{"C", 1})
		if err := <-e; err != nil {
			t.Fatalf("E = %v", err)
		}
		if want := "[A B C E]"; fmt.Sprint(order) != want {
			t.Fatalf("order = %v, want %s", order, want)
		}
	})
}
