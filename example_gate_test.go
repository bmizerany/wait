package wait_test

import (
	"context"
	"fmt"
	"log"

	"blake.io/wait"
)

// gate admits demands of type D in arrival order against capacity of type
// C: acquire waits its turn, and release gives a demand back. take reports
// whether d fits in c and, if so, what is left; give returns d to c. A
// demand at the front of the line holds back every demand behind it until
// it fits.
//
// The capacity is the only item in one List, so holding it is your turn.
// Capacity given back waits in a second List until the turn holder needs
// it. Neither List creates items, so no Ticket needs releasing: whoever
// holds an item owns it.
func gate[C, D any](capacity C, take func(C, D) (C, bool), give func(C, D) C) (acquire func(context.Context, D) error, release func(D)) {
	var turn wait.List[C]
	var back wait.List[D]
	turn.Add(capacity)

	acquire = func(ctx context.Context, d D) error {
		c, err := turn.Take(ctx).Value()
		if err != nil {
			return err
		}
		for {
			if rest, ok := take(c, d); ok {
				turn.Add(rest) // pass the turn, and what is left, on
				return nil
			}
			r, err := back.Take(ctx).Value()
			if err != nil {
				turn.Add(c) // give up the turn, keeping what came back
				return err
			}
			c = give(c, r)
		}
	}
	release = func(d D) { back.Add(d) }
	return acquire, release
}

type shape struct{ cpus, disk int }

func Example_gate() {
	acquire, release := gate(shape{16, 500},
		func(c, d shape) (shape, bool) {
			return shape{c.cpus - d.cpus, c.disk - d.disk}, d.cpus <= c.cpus && d.disk <= c.disk
		},
		func(c, d shape) shape { return shape{c.cpus + d.cpus, c.disk + d.disk} })
	ctx := context.Background()

	big := shape{8, 400}
	if err := acquire(ctx, big); err != nil {
		log.Fatal(err)
	}
	fmt.Println("admitted", big)

	small := shape{4, 200} // its CPUs fit, but its disk does not
	done := make(chan struct{})
	go func() {
		if err := acquire(ctx, small); err != nil {
			log.Fatal(err)
		}
		fmt.Println("admitted", small)
		close(done)
	}()

	fmt.Println("releasing", big)
	release(big)
	<-done

	// Output:
	// admitted {8 400}
	// releasing {8 400}
	// admitted {4 200}
}
