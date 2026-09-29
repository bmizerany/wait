package wait_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"blake.io/wait"
)

// This example loads copies of a model only when callers need them, up to
// max. A caller that finds no idle copy starts a load if there is room for
// another copy, then waits in line. Each copy, whether newly loaded or
// given back, goes to the caller that has waited longest.
//
// Calling TryTake before Take is fair, because TryTake cannot take a copy
// that a waiting caller is owed. If a copy is given back between TryTake
// and Take, the caller may start a load it didn't need, but there are
// still at most max copies.
//
// A load can fail for reasons the process doesn't control, such as other
// processes filling the GPU's memory. The goroutine that started the load
// tries again rather than giving up its room, because callers already in
// line are waiting for that copy.
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

	var misses atomic.Int64 // each of the first max misses starts a load
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
