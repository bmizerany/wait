package queue

import "testing"

func TestFifo(t *testing.T) {
	var q Fifo[int]

	if q.Len() != 0 {
		t.Fatalf("expected 0, got %d", q.Len())
	}

	if _, ok := q.Shift(); ok {
		t.Fatalf("expected false, got true")
	}

	checkOk := func(want int) {
		t.Helper()
		v, ok := q.Shift()
		if !ok {
			t.Fatalf("expected ok")
		}
		if v != want {
			t.Fatalf("expected %d, got %d", want, v)
		}
	}

	q.Unshift(1)
	q.Unshift(2)
	q.Unshift(3)

	if q.Len() != 3 {
		t.Fatalf("expected 3, got %d", q.Len())
	}

	checkOk(1)
	checkOk(2)

	q.Unshift(4)
	checkOk(3)
	checkOk(4)

	if v, ok := q.Shift(); ok {
		t.Fatalf("unexpected %d", v)
	}

	if q.Len() != 0 {
		t.Fatalf("expected 0, got %d", q.Len())
	}

	q.Unshift(1)
	checkOk(1)

	if q.Len() != 0 {
		t.Fatalf("expected 0, got %d", q.Len())
	}
}

func TestFifoShiftReleasesTail(t *testing.T) {
	var q Fifo[*int]
	q.Unshift(new(int))
	q.Unshift(new(int))
	q.Unshift(new(int))

	for range 3 {
		q.Shift()
		for i, p := range q.a[len(q.a):cap(q.a)] {
			if p != nil {
				t.Fatalf("slot %d beyond Len() retains %p, want nil", len(q.a)+i, p)
			}
		}
	}
}

func TestFifoFront(t *testing.T) {
	var q Fifo[int]

	if _, ok := q.Front(); ok {
		t.Fatal("Front() on empty queue = true, want false")
	}

	q.Unshift(1)
	q.Unshift(2)
	q.Unshift(3)

	if got, ok := q.Front(); !ok || got != 1 {
		t.Fatalf("Front() = (%d, %t), want (1, true)", got, ok)
	}

	if q.Len() != 3 {
		t.Fatalf("Len() after Front() = %d, want 3", q.Len())
	}

	if got, ok := q.Shift(); !ok || got != 1 {
		t.Fatalf("first Shift() = (%d, %t), want (1, true)", got, ok)
	}

	if got, ok := q.Front(); !ok || got != 2 {
		t.Fatalf("Front() after Shift() = (%d, %t), want (2, true)", got, ok)
	}

	if got, ok := q.Shift(); !ok || got != 2 {
		t.Fatalf("second Shift() = (%d, %t), want (2, true)", got, ok)
	}

	if got, ok := q.Shift(); !ok || got != 3 {
		t.Fatalf("third Shift() = (%d, %t), want (3, true)", got, ok)
	}
}

func TestLifo(t *testing.T) {
	var q Lifo[int]

	if q.Len() != 0 {
		t.Fatalf("expected 0, got %d", q.Len())
	}

	if _, ok := q.Pop(); ok {
		t.Fatalf("expected false, got true")
	}

	checkOk := func(want int) {
		t.Helper()
		v, ok := q.Pop()
		if !ok {
			t.Fatalf("expected ok")
		}
		if v != want {
			t.Fatalf("expected %d, got %d", want, v)
		}
	}

	q.Push(1)
	q.Push(2)
	q.Push(3)

	if q.Len() != 3 {
		t.Fatalf("expected 3, got %d", q.Len())
	}

	checkOk(3)
	checkOk(2)
	q.Push(4)
	checkOk(4)
	checkOk(1)

	if _, ok := q.Pop(); ok {
		t.Fatalf("expected false, got true")
	}

	if q.Len() != 0 {
		t.Fatalf("expected 0, got %d", q.Len())
	}

	q.Push(1)
	checkOk(1)

	if q.Len() != 0 {
		t.Fatalf("expected 0, got %d", q.Len())
	}
}
