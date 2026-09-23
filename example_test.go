package wait_test

import (
	"context"
	"fmt"
	"log"

	"blake.io/wait"
)

// A List of two connections. A released connection is the next one
// taken, while it is still warm.
func ExampleList() {
	var conns wait.List[string]
	conns.Add("conn-a")
	conns.Add("conn-b")

	for range 2 {
		t := conns.Take(context.Background())
		c, err := t.Value()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("using", c)
		t.Release()
	}

	// Output:
	// using conn-b
	// using conn-b
}

// A connection that fails a health check is retired instead of released.
// The deferred Release then does nothing, and the List creates a
// replacement for the next caller.
func ExampleTicket_Retire() {
	n := 0
	conns := &wait.List[string]{
		MaxItems: 1,
		New: func() string {
			n++
			return fmt.Sprintf("conn-%d", n)
		},
	}

	query := func(healthy bool) error {
		t := conns.Take(context.Background())
		defer t.Release()
		c, err := t.Value()
		if err != nil {
			return err
		}
		fmt.Println("using", c)
		if !healthy {
			t.Retire()
			return fmt.Errorf("%s: broken", c)
		}
		return nil
	}

	if err := query(false); err != nil {
		fmt.Println("error:", err)
	}
	if err := query(true); err != nil {
		fmt.Println("error:", err)
	}

	// Output:
	// using conn-1
	// error: conn-1: broken
	// using conn-2
}

// A Gate over a budget of 10 bytes. Each admitted request holds its bytes
// until it releases its Ticket.
func ExampleGate() {
	budget := 10
	g := &wait.Gate[int]{
		Claim: func(n int) bool {
			if n > budget {
				return false
			}
			budget -= n
			return true
		},
		Release: func(n int) { budget += n },
	}

	tk := g.Take(context.Background(), 8)
	if _, err := tk.Value(); err != nil {
		log.Fatal(err)
	}

	// A ctx that is already done asks only for what fits now.
	now, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := g.Take(now, 4).Value(); err != nil {
		fmt.Println("4 does not fit beside 8")
	}

	tk.Release()
	tk.Release() // no effect: the 8 bytes come back once

	if _, err := g.Take(now, 11).Value(); err != nil {
		fmt.Println("11 does not fit in 10")
	}
	tk = g.Take(now, 10)
	if _, err := tk.Value(); err == nil {
		fmt.Println("10 fits")
	}
	tk.Release()

	// Output:
	// 4 does not fit beside 8
	// 11 does not fit in 10
	// 10 fits
}
