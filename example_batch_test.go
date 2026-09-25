package wait_test

import (
	"context"
	"fmt"
	"log"
	"sync"

	"blake.io/wait"
)

// Seven callers share GPU batches of 16 floats, in arrival order. A batch
// is a slice whose length is the part reserved so far. The open batch is
// the only item in turn, so holding it is your turn to reserve. A caller
// that doesn't fit takes a place in next before it passes the turn on:
// smaller callers behind it fill the batch, and it goes first in the next
// one. Each caller's view is a subslice of its batch, not a copy.
func Example_batch() {
	ctx := context.Background()
	var turn, next wait.List[[]float32]
	turn.Add(make([]float32, 0, 16))

	views := make([][]float32, 7)
	var wg sync.WaitGroup
	submit := func(i, n int) {
		t := turn.Take(ctx) // Take never blocks, so the call order is the arrival order
		wg.Go(func() {
			l := &turn
			b, err := t.Value()
			for err == nil && n > cap(b)-len(b) {
				l = &next
				aside := l.Take(ctx) // keep a place before passing the turn on
				t.Release()
				t = aside
				b, err = t.Value()
			}
			if err != nil {
				log.Fatal(err)
			}
			views[i] = b[len(b) : len(b)+n]
			l.Add(b[:len(b)+n]) // pass the turn on, n more reserved
		})
	}

	// run waits its turn, so everyone ahead of it has reserved or stepped
	// aside, fills the batch, and opens the next one: first to those who
	// stepped aside, then to everyone else.
	run := func(k int) {
		b, err := turn.Take(ctx).Value()
		if err != nil {
			log.Fatal(err)
		}
		mine := next.Take(ctx) // behind everyone who stepped aside
		fmt.Printf("batch %d runs %d floats\n", k, len(b))
		for i := range b {
			b[i] = float32(100*k + i)
		}
		next.Add(make([]float32, 0, 16))
		if b, err = mine.Value(); err != nil {
			log.Fatal(err)
		}
		turn.Add(b)
	}

	for i, n := range []int{6, 8, 12, 2, 5, 3} {
		submit(i, n)
	}
	run(1)
	submit(6, 4) // arrives after batch 1
	run(2)
	run(3)
	wg.Wait()
	for i, v := range views {
		fmt.Printf("caller %d: %v\n", i+1, v)
	}

	// Output:
	// batch 1 runs 16 floats
	// batch 2 runs 15 floats
	// batch 3 runs 9 floats
	// caller 1: [100 101 102 103 104 105]
	// caller 2: [106 107 108 109 110 111 112 113]
	// caller 3: [200 201 202 203 204 205 206 207 208 209 210 211]
	// caller 4: [114 115]
	// caller 5: [300 301 302 303 304]
	// caller 6: [212 213 214]
	// caller 7: [305 306 307 308]
}
