package wait_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"blake.io/wait"
)

// A scratch is disk space reserved for spilling data that doesn't fit in
// memory. Reserving it can fail for reasons outside the process, like a
// full disk, and a scratch that failed holds the error.
type scratch struct {
	name  string
	tries int
	err   error
}

// reserve stands in for creating and preallocating a file. Every other
// try fails.
func (s *scratch) reserve() {
	s.tries++
	s.err = nil
	if s.tries%2 == 1 {
		s.err = errors.New("no space left on device")
	}
}

// Scratch files are made on demand, up to max. A caller that finds none
// idle starts making one, if there is room for another, and waits in line
// for it or one given back, whichever comes first. The new one goes to
// whoever has waited longest.
//
// Looking before joining the line is fair: TryTake cannot take a scratch
// a waiter is owed. At worst, one given back between TryTake and Take
// means one made sooner than needed, never more than max.
//
// A scratch that fails still goes into the List, holding its error, so its
// place under max isn't lost and no one waits for one that never comes.
// Whoever gets it tries again; if that fails too, Done hands it on.
func Example_createOnDemand() {
	const max = 2
	ctx := context.Background()
	var files wait.List[*scratch]
	var made atomic.Int64 // callers that found none idle; the first max make one

	get := func() wait.Ticket[*scratch] {
		if t, ok := files.TryTake(); ok {
			return t // an idle scratch
		}
		if n := made.Add(1); n <= max {
			go func() {
				s := &scratch{name: fmt.Sprintf("scratch-%d", n)}
				s.reserve()
				files.Add(s)
			}()
		}
		return files.Take(ctx) // at max: wait for one to come back
	}
	use := func(caller string, t wait.Ticket[*scratch]) {
		s, _ := t.Value() // ctx is never done, so Value never fails
		if s.err != nil {
			fmt.Printf("%s: %s: %v; trying again\n", caller, s.name, s.err)
			s.reserve()
		}
		if s.err != nil {
			t.Done() // the next caller tries again
			return
		}
		fmt.Println(caller, "spilling to", s.name)
	}

	a := get()
	use("a", a)
	b := get() // scratch-1 is in use: makes scratch-2
	use("b", b)
	c := get() // at max: waits in line
	a.Done()   // scratch-1 goes to c
	use("c", c)
	b.Done()
	c.Done()

	// Output:
	// a: scratch-1: no space left on device; trying again
	// a spilling to scratch-1
	// b: scratch-2: no space left on device; trying again
	// b spilling to scratch-2
	// c spilling to scratch-1
}
