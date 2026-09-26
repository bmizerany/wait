package wait_test

import (
	"context"
	"fmt"
	"log"

	"blake.io/wait"
)

// A List of two connections. A returned connection is the next one
// taken, while it is still warm.
func ExampleList() {
	var conns wait.List[string]
	conns.Add("conn-a")
	conns.Add("conn-b")

	use := func() {
		t := conns.Take(context.Background())
		defer t.Done()
		c, err := t.Value()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("using", c)
	}
	use()
	use()

	// Output:
	// using conn-b
	// using conn-b
}

// At shutdown, Close the List and drain its ready items. After Close, Take
// returns each ready item, then fails with ErrClosed.
func ExampleList_Close() {
	var conns wait.List[string]
	conns.Add("conn-a")
	conns.Add("conn-b")

	conns.Close()
	for {
		c, err := conns.Take(context.Background()).Value()
		if err != nil {
			fmt.Println(err)
			break
		}
		fmt.Println("closing", c) // not given back: it is ours to close
	}

	// Output:
	// closing conn-b
	// closing conn-a
	// closed
}

// TryTake takes an idle item if there is one, and never joins the line.
func ExampleList_TryTake() {
	var conns wait.List[string]
	conns.Add("conn-a")

	if t, ok := conns.TryTake(); ok {
		defer t.Done()
		c, _ := t.Value()
		fmt.Println("took", c)
	}
	if _, ok := conns.TryTake(); !ok {
		fmt.Println("no conn ready")
	}

	// Output:
	// took conn-a
	// no conn ready
}

// A caller that finds its connection broken replaces it. Leave keeps the
// broken one from going back to the List, and Add puts a new one in its
// place. The deferred Done still ends the Ticket, but gives nothing
// back.
func ExampleTicket_Leave() {
	var conns wait.List[string]
	dials := 1
	conns.Add("conn-1")

	use := func(broken bool) {
		t := conns.Take(context.Background())
		defer t.Done()
		c, err := t.Value()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("using", c)
		if broken && t.Leave() {
			dials++
			conns.Add(fmt.Sprintf("conn-%d", dials))
		}
	}
	use(true) // conn-1 breaks
	use(false)

	// Output:
	// using conn-1
	// using conn-2
}
