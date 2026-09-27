package batch_test

import (
	"context"
	"fmt"
	"log"

	"blake.io/wait/batch"
)

// Three callers bring token IDs, and each wants two floats back. The owner
// finds each caller's tokens and output by the batch's offsets and writes,
// standing in for an embedding model, the number of tokens and their sum.
// Each caller reads its own output, then gives it back with Done so the
// buffer can be reused.
func Example() {
	ctx := context.Background()
	b := batch.New[uint32, float32](16, 6, 2)

	var hs []batch.Handle[uint32, float32]
	for _, tokens := range [][]uint32{{1, 2}, {3, 4, 5}, {6}} {
		h, err := b.Reserve(ctx, tokens, 2)
		if err != nil {
			log.Fatal(err)
		}
		hs = append(hs, h)
	}

	// The output is full, so Batches yields the batch at once.
	for bt := range b.Batches(ctx) {
		fmt.Printf("running %d tokens for %d callers\n", len(bt.In), len(bt.InOffsets)-1)
		for i := range len(bt.InOffsets) - 1 {
			tokens := bt.In[bt.InOffsets[i]:bt.InOffsets[i+1]]
			out := bt.Out[bt.OutOffsets[i]:bt.OutOffsets[i+1]]
			out[0] = float32(len(tokens))
			for _, tok := range tokens {
				out[1] += float32(tok)
			}
		}
		break
	}

	for i, h := range hs {
		emb, err := h.Wait(ctx)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("caller %d: %v\n", i+1, emb)
		h.Done()
	}

	// Output:
	// running 6 tokens for 3 callers
	// caller 1: [2 3]
	// caller 2: [3 12]
	// caller 3: [1 6]
}
