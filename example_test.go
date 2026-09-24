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

	for range 2 {
		t := conns.Take(context.Background())
		c, err := t.Value()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("using", c)
		t.Release()
	}

	// Output:
	// using conn-b
	// using conn-b
}

// At shutdown, Close the List and drain its ready items. After Close, Take
// never waits, whatever its ctx: it returns each ready item, then fails
// with ErrClosed.
func ExampleList_Close() {
	var conns wait.List[string]
	conns.Add("conn-a")
	conns.Add("conn-b")

	conns.Close()
	for {
		t := conns.Take(context.Background())
		c, err := t.Value()
		if err != nil {
			fmt.Println(err)
			break
		}
		fmt.Println("closing", c) // not released: it is ours to close
	}

	// Output:
	// closing conn-b
	// closing conn-a
	// closed
}
