package wait_test

import (
	"context"
	"fmt"

	"blake.io/wait"
)

// Downloads share a link's 8 connections and 100 Mbps, strictly in the
// order they ask. The second download's connections fit beside the
// first's, but its bandwidth doesn't, so it waits for the first to finish.
//
// The ctx here is never done, so Value never fails. Code whose ctx can end
// must pass the turn on, with what came back, when it gives up.
func Example_demand() {
	type demand struct{ conns, mbps int }
	ctx := context.Background()

	link := demand{8, 100}

	// The link's free capacity is the only item in free, so holding it is
	// your turn.
	var free, returned wait.List[demand]
	free.Add(link)

	// acquire reports false for more than the whole link, which would hold
	// the turn forever, and everyone behind it with it.
	acquire := func(d demand) bool {
		if d.conns > link.conns || d.mbps > link.mbps {
			return false
		}
		f, _ := free.Take(ctx).Value() // wait your turn
		for d.conns > f.conns || d.mbps > f.mbps {
			r, _ := returned.Take(ctx).Value() // hold the turn until enough is back
			f.conns, f.mbps = f.conns+r.conns, f.mbps+r.mbps
		}
		free.Add(demand{f.conns - d.conns, f.mbps - d.mbps}) // pass the turn on
		return true
	}

	if !acquire(demand{16, 10}) {
		fmt.Println("16 connections is more than the link has")
	}
	for _, d := range []demand{{4, 60}, {2, 50}} {
		acquire(d)
		go func() {
			defer returned.Add(d)
			fmt.Printf("downloading on %d connections at %d Mbps\n", d.conns, d.mbps)
		}()
	}
	acquire(link) // the whole link: waits for both downloads

	// Output:
	// 16 connections is more than the link has
	// downloading on 4 connections at 60 Mbps
	// downloading on 2 connections at 50 Mbps
}
