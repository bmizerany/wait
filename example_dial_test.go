package wait_test

import (
	"context"
	"fmt"
	"sync/atomic"

	"blake.io/wait"
)

// Connections are dialed on demand, up to max. A caller that finds none
// idle starts a dial, if there is room for another connection, and waits
// in line for the new one or one given back, whichever comes first. The
// new one goes to whoever has waited longest.
//
// Looking before joining the line is fair: TryTake cannot take a
// connection a waiter is owed. At worst, a connection given back between
// TryTake and Take means one dial more than needed, never more than max.
// A real dial can fail; then it must undo its made.Add(1).
func Example_dialOnDemand() {
	const max = 2
	ctx := context.Background()
	var conns wait.List[string]
	var made atomic.Int64 // connections dialed, or being dialed

	get := func() wait.Ticket[string] {
		if t, ok := conns.TryTake(); ok {
			return t // an idle connection
		}
		if n := made.Add(1); n <= max {
			go conns.Add(fmt.Sprintf("conn-%d", n)) // dial
		} else {
			made.Add(-1) // at max: wait for one to come back
		}
		return conns.Take(ctx)
	}
	use := func(caller string, t wait.Ticket[string]) {
		c, _ := t.Value() // ctx is never done, so Value never fails
		fmt.Println(caller, "using", c)
	}

	a := get()
	use("a", a) // dials conn-1
	b := get()
	use("b", b) // conn-1 is in use: dials conn-2
	c := get()  // at max: waits in line
	a.Done()    // conn-1 goes to c
	use("c", c)
	b.Done()
	c.Done()
	fmt.Println("dialed", made.Load())

	// Output:
	// a using conn-1
	// b using conn-2
	// c using conn-1
	// dialed 2
}
