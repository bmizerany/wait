package wait

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
)

func TestListReserveImmediateErrors(t *testing.T) {
	t.Run("max waiters", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			p := &List[int]{MaxItems: 1, MaxWaiters: 1}
			if _, err := p.Take(t.Context()); err != nil {
				t.Fatal("occupy item:", err)
			}

			first, err := p.Reserve(t.Context())
			if err != nil {
				t.Fatal("first Reserve:", err)
			}
			if _, err := p.Reserve(t.Context()); !errors.Is(err, ErrMaxWaiters) {
				t.Fatalf("second Reserve error = %v, want ErrMaxWaiters", err)
			}

			p.Close()
			if _, err := first.Wait(); !errors.Is(err, ErrClosed) {
				t.Errorf("first Wait error = %v, want ErrClosed", err)
			}
		})
	})

	t.Run("closed", func(t *testing.T) {
		var p List[int]
		p.Close()
		if future, err := p.Reserve(t.Context()); future != nil || !errors.Is(err, ErrClosed) {
			t.Fatalf("Reserve() = (%v, %v), want (nil, ErrClosed)", future, err)
		}
	})

	t.Run("initially canceled", func(t *testing.T) {
		var p List[int]
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if future, err := p.Reserve(ctx); future != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("Reserve() = (%v, %v), want (nil, context.Canceled)", future, err)
		}
	})
}

func TestListReserveRegistersFIFO(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &List[int]{MaxItems: 1, MaxWaiters: 2}
		if _, err := p.Take(t.Context()); err != nil {
			t.Fatal("occupy item:", err)
		}

		first, err := p.Reserve(t.Context())
		if err != nil {
			t.Fatal("first Reserve:", err)
		}
		second, err := p.Reserve(t.Context())
		if err != nil {
			t.Fatal("second Reserve:", err)
		}

		// Both reservations are already in the queue when Reserve returns.
		if !p.Put(11) || !p.Put(22) {
			t.Fatal("Put rejected an item")
		}
		if got, err := first.Wait(); err != nil || got != 11 {
			t.Errorf("first Wait() = (%d, %v), want (11, nil)", got, err)
		}
		if got, err := second.Wait(); err != nil || got != 22 {
			t.Errorf("second Wait() = (%d, %v), want (22, nil)", got, err)
		}
	})
}

func TestListReserveCancellationRemovesReservation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &List[int]{MaxItems: 1, MaxWaiters: 1}
		if _, err := p.Take(t.Context()); err != nil {
			t.Fatal("occupy item:", err)
		}

		ctx, cancel := context.WithCancel(t.Context())
		canceled, err := p.Reserve(ctx)
		if err != nil {
			t.Fatal("Reserve canceled future:", err)
		}
		cancel()
		// Let asynchronous context callbacks and queue cleanup become quiescent.
		synctest.Wait()

		// No one has called canceled.Wait(). Cancellation alone frees the slot.
		next, err := p.Reserve(t.Context())
		if err != nil {
			t.Fatalf("Reserve after cancellation = %v, want nil", err)
		}
		if !p.Put(33) {
			t.Fatal("Put rejected an item")
		}
		if got, err := canceled.Wait(); !errors.Is(err, context.Canceled) || got != 0 {
			t.Errorf("canceled Wait() = (%d, %v), want (0, context.Canceled)", got, err)
		}
		if got, err := next.Wait(); err != nil || got != 33 {
			t.Errorf("next Wait() = (%d, %v), want (33, nil)", got, err)
		}
	})
}

func TestListReservePrunesCanceledReservationBeforeCallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &List[int]{MaxItems: 1, MaxWaiters: 1}
		if _, err := p.Take(t.Context()); err != nil {
			t.Fatal("occupy item:", err)
		}

		ctx, cancel := context.WithCancel(t.Context())
		canceled, err := p.Reserve(ctx)
		if err != nil {
			t.Fatal("Reserve canceled future:", err)
		}

		// Stop the registered callback before cancellation so only Reserve's
		// synchronous queue cleanup can free the MaxWaiters slot.
		p.waitersMu.Lock()
		w, ok := p.waiters.Front()
		if !ok || w.future != canceled {
			p.waitersMu.Unlock()
			t.Fatal("reserved future is not at the head of the wait queue")
		}
		if w.stop == nil || !w.stop() {
			p.waitersMu.Unlock()
			t.Fatal("failed to stop cancellation callback before it ran")
		}
		p.waitersMu.Unlock()

		cancel()
		next, err := p.Reserve(t.Context())
		if err != nil {
			t.Fatalf("Reserve before cancellation callback = %v, want nil", err)
		}
		if got, err := canceled.Wait(); got != 0 || !errors.Is(err, context.Canceled) {
			t.Errorf("canceled Wait() = (%d, %v), want (0, context.Canceled)", got, err)
		}
		if !p.Put(34) {
			t.Fatal("Put rejected an item")
		}
		if got, err := next.Wait(); got != 34 || err != nil {
			t.Errorf("next Wait() = (%d, %v), want (34, nil)", got, err)
		}
	})
}

func TestListFutureWaitReturnsCancelCause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &List[int]{MaxItems: 1}
		if _, err := p.Take(t.Context()); err != nil {
			t.Fatal("occupy item:", err)
		}

		ctx, cancel := context.WithCancelCause(t.Context())
		future, err := p.Reserve(ctx)
		if err != nil {
			t.Fatal("Reserve:", err)
		}

		cause := errors.New("reservation no longer needed")
		cancel(cause)
		synctest.Wait()
		if got, err := future.Wait(); got != 0 || err != cause {
			t.Errorf("Wait() = (%d, %v), want (0, %v)", got, err, cause)
		}
	})
}

func TestListFutureCloseAfterReservation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &List[int]{MaxItems: 1}
		if _, err := p.Take(t.Context()); err != nil {
			t.Fatal("occupy item:", err)
		}
		future, err := p.Reserve(t.Context())
		if err != nil {
			t.Fatal("Reserve:", err)
		}

		p.Close()
		if got, err := future.Wait(); !errors.Is(err, ErrClosed) || got != 0 {
			t.Errorf("Wait() = (%d, %v), want (0, ErrClosed)", got, err)
		}
	})
}

func TestListFutureItemWinsAfterHandoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &List[int]{MaxItems: 1}
		if _, err := p.Take(t.Context()); err != nil {
			t.Fatal("occupy item:", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		future, err := p.Reserve(ctx)
		if err != nil {
			t.Fatal("Reserve:", err)
		}

		// Complete the reservation before canceling its context. Once an item
		// has been assigned, cancellation cannot revoke ownership.
		if !p.Put(44) {
			t.Fatal("Put rejected an item")
		}
		cancel()
		if got, err := future.Wait(); err != nil || got != 44 {
			t.Errorf("Wait() = (%d, %v), want (44, nil)", got, err)
		}
	})
}

func TestListFutureConcurrentWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &List[int]{MaxItems: 1}
		if _, err := p.Take(t.Context()); err != nil {
			t.Fatal("occupy item:", err)
		}
		future, err := p.Reserve(t.Context())
		if err != nil {
			t.Fatal("Reserve:", err)
		}

		const waiters = 5
		results := make(chan struct {
			value int
			err   error
		}, waiters)
		for range waiters {
			go func() {
				v, err := future.Wait()
				results <- struct {
					value int
					err   error
				}{v, err}
			}()
		}
		synctest.Wait()

		if !p.Put(55) {
			t.Fatal("Put rejected an item")
		}
		synctest.Wait()
		for range waiters {
			got := <-results
			if got.err != nil || got.value != 55 {
				t.Errorf("Wait() = (%d, %v), want (55, nil)", got.value, got.err)
			}
		}
		if got, err := future.Wait(); err != nil || got != 55 {
			t.Errorf("repeated Wait() = (%d, %v), want (55, nil)", got, err)
		}
	})
}

func TestListReserveReadyItemPrecedesClosedAndCanceled(t *testing.T) {
	var p List[int]
	if !p.Put(66) {
		t.Fatal("Put rejected an item")
	}
	p.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	future, err := p.Reserve(ctx)
	if err != nil {
		t.Fatalf("Reserve with ready item, closed list, and canceled context: %v", err)
	}
	if got, err := future.Wait(); err != nil || got != 66 {
		t.Errorf("Wait() = (%d, %v), want (66, nil)", got, err)
	}
}

func TestListFutureCloseRacesWithPut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for range 20 {
			p := &List[int]{MaxItems: 1}
			if _, err := p.Take(t.Context()); err != nil {
				t.Fatal("occupy item:", err)
			}
			future, err := p.Reserve(t.Context())
			if err != nil {
				t.Fatal("Reserve:", err)
			}

			start := make(chan struct{})
			go func() {
				<-start
				p.Put(77)
			}()
			go func() {
				<-start
				p.Close()
			}()
			close(start)
			synctest.Wait()

			got, err := future.Wait()
			if err == nil {
				if got != 77 {
					t.Errorf("Wait() = (%d, nil), want (77, nil)", got)
				}
			} else if !errors.Is(err, ErrClosed) {
				t.Errorf("Wait() error = %v, want nil or ErrClosed", err)
			}
			if gotAgain, errAgain := future.Wait(); gotAgain != got || !errors.Is(errAgain, err) {
				t.Errorf("repeated Wait() = (%d, %v), want (%d, %v)", gotAgain, errAgain, got, err)
			}
		}
	})
}
