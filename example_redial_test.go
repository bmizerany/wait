package wait_test

import (
	"context"
	"fmt"
	"log"
	"sync"

	"blake.io/wait"
)

// A conn is a pooled connection that redials itself. It dials in the
// background when it is made and again after redial, so whoever takes it
// from a List usually finds it connected.
type conn struct {
	addr   string
	dials  int
	dialed func() (string, error) // waits for the dial in flight
}

func newConn(addr string) *conn {
	c := &conn{addr: addr}
	c.redial()
	return c
}

func (c *conn) redial() {
	c.dials++
	session := fmt.Sprintf("%s#%d", c.addr, c.dials) // stands in for a real dial
	c.dialed = sync.OnceValues(func() (string, error) { return session, nil })
	go c.dialed()
}

// A caller that finds its connection broken redials it before the deferred
// Release gives it back, so the next caller gets a fresh one.
func Example_redial() {
	var conns wait.List[*conn]
	conns.Add(newConn("db"))

	use := func(broken bool) {
		t := conns.Take(context.Background())
		defer t.Release()
		c, err := t.Value()
		if err != nil {
			log.Fatal(err)
		}
		session, err := c.dialed()
		if err != nil {
			c.redial()
			return
		}
		fmt.Println("using", session)
		if broken {
			c.redial()
		}
	}

	use(true) // db#1 breaks while in use
	use(false)

	// Output:
	// using db#1
	// using db#2
}
