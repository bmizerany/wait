package wait_test

import (
	"context"
	"fmt"
	"log"

	"blake.io/wait"
)

// A Spec is what a VM needs from its host.
type Spec struct{ CPUs, MemoryGB int }

func (s Spec) fits(free Spec) bool { return s.CPUs <= free.CPUs && s.MemoryGB <= free.MemoryGB }
func (s Spec) plus(t Spec) Spec    { return Spec{s.CPUs + t.CPUs, s.MemoryGB + t.MemoryGB} }
func (s Spec) minus(t Spec) Spec   { return Spec{s.CPUs - t.CPUs, s.MemoryGB - t.MemoryGB} }

// A Host runs VMs while their Specs fit its capacity, serving callers
// strictly in arrival order. The free capacity is the only item in one
// List, so holding it is your turn; capacity given back waits in a second
// List until the turn holder needs it.
type Host struct {
	turn wait.List[Spec] // free capacity
	back wait.List[Spec] // capacity given back
}

func NewHost(capacity Spec) *Host {
	h := new(Host)
	h.turn.Add(capacity)
	return h
}

// Acquire waits its turn, then waits until s fits and takes it. Everyone
// behind it waits too, even for a Spec that would fit.
func (h *Host) Acquire(ctx context.Context, s Spec) error {
	free, err := h.turn.Take(ctx).Value()
	if err != nil {
		return err
	}
	for !s.fits(free) {
		back, err := h.back.Take(ctx).Value()
		if err != nil {
			h.turn.Add(free) // give up the turn, keeping what came back
			return err
		}
		free = free.plus(back)
	}
	h.turn.Add(free.minus(s))
	return nil
}

// Release gives s back to the Host.
func (h *Host) Release(s Spec) { h.back.Add(s) }

// A host with 16 CPUs and 64 GB of memory. The second VM's CPUs fit beside
// the first, but its memory does not, so it waits its turn.
func Example_vms() {
	host := Spec{CPUs: 16, MemoryGB: 64}
	h := NewHost(host)
	ctx := context.Background()

	for _, s := range []Spec{
		{CPUs: 8, MemoryGB: 48},
		{CPUs: 4, MemoryGB: 32},
	} {
		if err := h.Acquire(ctx, s); err != nil {
			log.Fatal(err)
		}
		go func() {
			defer h.Release(s)
			fmt.Println("running", s)
		}()
	}

	// Taking the whole host waits for every VM to stop.
	if err := h.Acquire(ctx, host); err != nil {
		log.Fatal(err)
	}

	// Output:
	// running {8 48}
	// running {4 32}
}
