package wait_test

import (
	"context"
	"fmt"
	"sync"

	"blake.io/wait"
)

type conn struct{ name string }

// A slot is a place in a List for one connection. It starts dialing when it
// is made, and again when its connection breaks, before anyone takes it, so
// whoever takes the slot usually finds the connection ready. Only the
// slot's holder touches it, so it needs no lock.
type slot struct {
	name  string
	dials int
	conn  func() (*conn, error) // the dial in flight, or its result
}

func newSlot(name string) *slot {
	s := &slot{name: name}
	s.redial()
	return s
}

// redial starts a fresh dial in the background.
func (s *slot) redial() {
	s.dials++
	c := &conn{name: fmt.Sprintf("%s#%d", s.name, s.dials)}
	dial := sync.OnceValues(func() (*conn, error) { return c, nil }) // a real dial goes here
	go dial()                                                        // warm it up now
	s.conn = dial
}

// Slots make a List dial lazily, keep connections warm, and replace broken
// ones, while the List only hands slots out in turn. Callers always defer
// Release; a broken connection's slot redials before it goes back.
func Example_slot() {
	var conns wait.List[*slot]
	conns.Add(newSlot("a"))

	query := func(fail bool) error {
		t := conns.Take(context.Background())
		defer t.Release()
		s, err := t.Value()
		if err != nil {
			return err
		}
		c, err := s.conn()
		if err != nil {
			s.redial()
			return err
		}
		fmt.Println("using", c.name)
		if fail {
			s.redial() // the next caller gets a fresh connection
			return fmt.Errorf("%s: broken", c.name)
		}
		return nil
	}

	if err := query(true); err != nil {
		fmt.Println("error:", err)
	}
	if err := query(false); err != nil {
		fmt.Println("error:", err)
	}

	// Output:
	// using a#1
	// error: a#1: broken
	// using a#2
}
