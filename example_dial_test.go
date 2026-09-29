package wait_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"blake.io/wait"
)

// A dialConn is a slot in the pool: connected, or holding the error of its
// last dial.
type dialConn struct {
	name string
	err  error
}

// Connections are dialed on demand, up to max. A caller that finds none
// idle starts a dial, if there is room for another connection, and waits
// in line for the new one or one given back, whichever comes first. The
// new one goes to whoever has waited longest.
//
// Looking before joining the line is fair: TryTake cannot take a
// connection a waiter is owed. At worst, a connection given back between
// TryTake and Take means one dial more than needed, never more than max.
//
// A dial that fails still adds its conn, holding the error, so the slot
// isn't lost and no one waits for a connection that never comes. Whoever
// gets it redials it; if that fails too, its deferred Done hands it on.
func Example_dialOnDemand() {
	const max = 2
	ctx := context.Background()
	var conns wait.List[*dialConn]
	var made atomic.Int64    // callers that found none idle; the first max dial
	var refused atomic.Int64 // dials still to refuse
	refused.Store(2)

	dial := func(c *dialConn) {
		c.err = nil
		if refused.Add(-1) >= 0 {
			c.err = errors.New("connection refused")
		}
	}
	get := func() wait.Ticket[*dialConn] {
		if t, ok := conns.TryTake(); ok {
			return t // an idle connection
		}
		if n := made.Add(1); n <= max {
			go func() {
				c := &dialConn{name: fmt.Sprintf("conn-%d", n)}
				dial(c)
				conns.Add(c)
			}()
		}
		return conns.Take(ctx) // at max: wait for one to come back
	}
	use := func(caller string, t wait.Ticket[*dialConn]) {
		c, _ := t.Value() // ctx is never done, so Value never fails
		if c.err != nil {
			dial(c)
		}
		if c.err != nil {
			fmt.Println(caller, "cannot dial:", c.err)
			t.Done() // hands the broken conn on
			return
		}
		fmt.Println(caller, "using", c.name)
	}

	a := get()
	use("a", a) // conn-1's dial failed, and so does the redial
	b := get()  // takes the broken conn-1 and redials it
	use("b", b)
	c := get() // conn-1 is in use: dials conn-2
	use("c", c)
	d := get() // at max: waits in line
	b.Done()   // conn-1 goes to d
	use("d", d)
	c.Done()
	d.Done()

	// Output:
	// a cannot dial: connection refused
	// b using conn-1
	// c using conn-2
	// d using conn-1
}
