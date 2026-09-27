package batch_test

import (
	"context"
	"fmt"
	"log"

	"blake.io/wait/batch"
)

// Three callers bring their inputs to a batch of 8 floats. The owner
// doubles each element in place, and each caller reads its results from
// its own part of the batch, then gives them back with Done so the buffer
// can be reused.
func Example() {
	ctx := context.Background()
	b := batch.New[float32](8, 2)

	var hs []batch.Handle[float32]
	for _, in := range [][]float32{{1, 2}, {3, 4, 5}, {6, 7, 8}} {
		h, err := b.Reserve(ctx, in)
		if err != nil {
			log.Fatal(err)
		}
		hs = append(hs, h)
	}

	// The batch is full, so Batches yields it at once.
	for mem := range b.Batches(ctx) {
		fmt.Printf("running %d floats\n", len(mem))
		for i := range mem {
			mem[i] *= 2
		}
		break
	}

	for i, h := range hs {
		mem, err := h.Wait(ctx)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("caller %d: %v\n", i+1, mem)
		h.Done()
	}

	// Output:
	// running 8 floats
	// caller 1: [2 4]
	// caller 2: [6 8 10]
	// caller 3: [12 14 16]
}
