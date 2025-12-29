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
