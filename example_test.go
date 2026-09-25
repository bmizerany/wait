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

	use := func() {
		t := conns.Take(context.Background())
		defer t.Release()
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

// At shutdown, Close the List and drain its ready items with TryTake,
// which takes them whatever the shutdown's ctx.
func ExampleList_Close() {
	var conns wait.List[string]
	conns.Add("conn-a")
	conns.Add("conn-b")

	conns.Close()
	for {
		t, ok := conns.TryTake()
		if !ok {
			break
		}
		c, _ := t.Value()
		fmt.Println("closing", c) // not released: it is ours to close
	}

	// Output:
	// closing conn-b
	// closing conn-a
}

// TryTake takes a ready item without waiting, and never joins the line.
func ExampleList_TryTake() {
	var conns wait.List[string]
	conns.Add("conn-a")

	if t, ok := conns.TryTake(); ok {
		defer t.Release()
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
