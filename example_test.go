package wait_test

import (
	"context"
	"fmt"
	"log"

	"blake.io/wait"
)

// A Gate over a budget of 10 bytes. Each admitted request holds its bytes
// until it releases the Grant that Wait or TryWait returned.
func ExampleGate() {
	budget := 10
	g := &wait.Gate[int]{
		Claim: func(n int) bool {
			if n > budget {
				return false
			}
			budget -= n
			return true
		},
		Release: func(n int) { budget += n },
	}

	grant, err := g.Wait(context.Background(), 8)
	if err != nil {
		log.Fatal(err)
	}
	if _, ok := g.TryWait(4); !ok {
		fmt.Println("4 does not fit beside 8")
	}

	grant.Release()
	grant.Release() // no effect: the 8 bytes come back once

	if _, ok := g.TryWait(11); !ok {
		fmt.Println("11 does not fit in 10")
	}
	if grant, ok := g.TryWait(10); ok {
		fmt.Println("10 fits")
		grant.Release()
	}

	// Output:
	// 4 does not fit beside 8
	// 11 does not fit in 10
	// 10 fits
}
