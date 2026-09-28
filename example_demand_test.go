package wait_test

import (
	"context"
	"fmt"
	"log"

	"blake.io/wait"
)

// Downloads share a link with 8 connections and 100 Mbps, served strictly
// in arrival order. The second download's connections fit beside the
// first's, but its bandwidth doesn't, so it waits its turn.
//
// The link's free capacity is the only item in turn, so holding it is
// your turn. Capacity given back waits in back until the turn's holder
// needs it, and everyone behind the holder waits too, even for a demand
// that would fit.
func Example_demand() {
	type demand struct{ conns, mbps int }
	link := demand{conns: 8, mbps: 100}
	var turn, back wait.List[demand]
	turn.Add(link)
	ctx := context.Background()

	acquire := func(d demand) error {
		free, err := turn.Take(ctx).Value()
		if err != nil {
			return err
		}
		for d.conns > free.conns || d.mbps > free.mbps {
			b, err := back.Take(ctx).Value()
			if err != nil {
				turn.Add(free) // give up the turn, keeping what came back
				return err
			}
			free.conns += b.conns
			free.mbps += b.mbps
		}
		turn.Add(demand{free.conns - d.conns, free.mbps - d.mbps})
		return nil
	}

	for _, d := range []demand{
		{conns: 4, mbps: 60},
		{conns: 2, mbps: 50},
	} {
		if err := acquire(d); err != nil {
			log.Fatal(err)
		}
		go func() {
			defer back.Add(d) // give the link back
			fmt.Printf("downloading on %d connections at %d Mbps\n", d.conns, d.mbps)
		}()
	}

	// Taking the whole link waits for every download to finish.
	if err := acquire(link); err != nil {
		log.Fatal(err)
	}

	// Output:
	// downloading on 4 connections at 60 Mbps
	// downloading on 2 connections at 50 Mbps
}
