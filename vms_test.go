package wait_test

import (
	"context"
	"testing"
	"testing/synctest"
)

// acquire starts an Acquire and returns a channel for its error, after the
// Acquire has either returned or started waiting.
func acquire(h *Host, ctx context.Context, s Spec) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- h.Acquire(ctx, s) }()
	synctest.Wait()
	return ch
}

func checkWaiting(t *testing.T, name string, ch <-chan error) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatalf("%s returned %v, want it waiting", name, err)
	default:
	}
}

var (
	host  = Spec{CPUs: 8, MemoryGB: 32}
	half  = Spec{CPUs: 4, MemoryGB: 16}
	whole = host
)

func TestHostFrontHoldsLine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewHost(host)
		ctx := t.Context()

		if err := h.Acquire(ctx, half); err != nil {
			t.Fatal(err)
		}
		b := acquire(h, ctx, whole) // waits at the front for all of it
		c := acquire(h, ctx, half)  // half is free, but c stays behind b
		checkWaiting(t, "b", b)
		checkWaiting(t, "c", c)

		h.Release(half) // all of it is free: b first
		if err := <-b; err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		checkWaiting(t, "c", c)

		h.Release(whole)
		if err := <-c; err != nil {
			t.Fatal(err)
		}
	})
}

func TestHostMemory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewHost(host)
		ctx := t.Context()

		if err := h.Acquire(ctx, Spec{CPUs: 2, MemoryGB: 24}); err != nil {
			t.Fatal(err)
		}
		c := acquire(h, ctx, Spec{CPUs: 2, MemoryGB: 16}) // CPUs fit; memory does not
		checkWaiting(t, "c", c)
		h.Release(Spec{CPUs: 2, MemoryGB: 24})
		if err := <-c; err != nil {
			t.Fatal(err)
		}
	})
}

func TestHostCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewHost(host)
		ctx := t.Context()

		if err := h.Acquire(ctx, half); err != nil {
			t.Fatal(err)
		}
		bctx, cancel := context.WithCancel(ctx)
		b := acquire(h, bctx, whole) // holds the turn, waiting
		c := acquire(h, ctx, half)
		cancel()
		if err := <-b; err != context.Canceled {
			t.Fatalf("b = %v, want context.Canceled", err)
		}
		if err := <-c; err != nil { // b passed the turn on with half still free
			t.Fatal(err)
		}
	})
}
