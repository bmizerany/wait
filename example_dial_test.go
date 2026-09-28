package wait_test

import (
	"context"
	"fmt"

	"blake.io/wait"
)

// Connections are dialed on demand, up to max. The List starts with max
// slots, none dialed, and a caller that takes an undialed slot dials it.
// Idle slots are stacked with the most recently used on top, so a caller
// finds an idle connection before an undialed slot: a new connection is
// dialed only when every dialed one is in use.
//
// A dial can fail. The slot stays undialed, and Done gives it back for the
// next caller in line to try. The List holds max slots, dialed or not,
// so there is no count to keep beside it.
func Example_dialOnDemand() {
	const max = 2
	ctx := context.Background()
	var slots wait.List[*string] // "" until dialed
	for range max {
		slots.Add(new(string))
	}

	dials := 0
	dial := func() (string, error) { // a real dial can fail
		dials++
		return fmt.Sprintf("conn-%d", dials), nil
	}
	// conn waits for t's slot and dials it if no one has yet.
	conn := func(t wait.Ticket[*string]) (string, error) {
		s, err := t.Value()
		if err != nil {
			return "", err
		}
		if *s == "" {
			c, err := dial()
			if err != nil {
				return "", err
			}
			*s = c
		}
		return *s, nil
	}
	use := func(caller string, t wait.Ticket[*string]) {
		c, _ := conn(t) // ctx is never done, and dial never fails
		fmt.Println(caller, "using", c)
	}

	a := slots.Take(ctx)
	use("a", a) // dials conn-1
	a.Done()
	b := slots.Take(ctx)
	use("b", b) // conn-1 is idle: no dial
	c := slots.Take(ctx)
	use("c", c)          // conn-1 is in use: dials conn-2
	d := slots.Take(ctx) // at max: waits in line
	b.Done()             // conn-1 goes to d
	use("d", d)
	c.Done()
	d.Done()
	fmt.Println("dialed", dials)

	// Output:
	// a using conn-1
	// b using conn-1
	// c using conn-2
	// d using conn-1
	// dialed 2
}
