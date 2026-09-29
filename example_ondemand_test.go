package wait_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"blake.io/wait"
)

// Copies of a model are loaded on demand, up to max. A caller that finds
// no copy idle starts loading one, if there is room, and waits in line.
// Each copy, new or given back, goes to whoever has waited longest.
//
// Looking first is fair: TryTake cannot take a copy a waiter is owed. At
// worst, a copy given back between TryTake and Take means one load more
// than needed, never more than max.
//
// Loading can fail for reasons outside the process, like other processes
// filling the GPU's memory. A failed load tries again instead of giving up
// its room, since callers in line are counting on it.
func Example_loadOnDemand() {
	const max = 2
	ctx := context.Background()
	var copies wait.List[string]

	// load stands in for reading the model into GPU memory. Every other
	// load finds the memory full.
	var loads atomic.Int64
	load := func() error {
		if loads.Add(1)%2 == 0 {
			return errors.New("out of GPU memory")
		}
		return nil
	}

	var misses atomic.Int64 // the first max each load a copy
	get := func() wait.Ticket[string] {
		if t, ok := copies.TryTake(); ok {
			return t // an idle copy
		}
		if n := misses.Add(1); n <= max {
			go func() {
				for err := load(); err != nil; err = load() {
					fmt.Printf("loading copy %d: %v; retrying\n", n, err)
					time.Sleep(100 * time.Millisecond)
				}
				copies.Add(fmt.Sprintf("copy %d", n))
			}()
		}
		return copies.Take(ctx)
	}
	use := func(caller string, t wait.Ticket[string]) {
		name, _ := t.Value() // ctx is never done, so Value never fails
		fmt.Println(caller, "running on", name)
	}

	a := get() // loads copy 1
	use("a", a)
	b := get() // copy 1 is in use: loads copy 2
	use("b", b)
	c := get() // at max: waits in line
	a.Done()   // copy 1 goes to c
	use("c", c)
	b.Done()
	c.Done()

	// Output:
	// a running on copy 1
	// loading copy 2: out of GPU memory; retrying
	// b running on copy 2
	// c running on copy 1
}
