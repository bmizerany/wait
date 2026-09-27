package queue

import (
	"slices"
	"testing"
)

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
		checkReleased(t, &q)
	}
}

// checkReleased fails t if a slot of q outside the queue holds a value.
func checkReleased(t *testing.T, q *Fifo[*int]) {
	t.Helper()
	for i, p := range q.a[:cap(q.a)] {
		if (i < q.head || i >= len(q.a)) && p != nil {
			t.Fatalf("slot %d outside the queue retains %p, want nil", i, p)
		}
	}
}

func TestFifoChurn(t *testing.T) {
	// A queue kept at a steady length moves values down as it goes. It
	// keeps their order, releases what it removed, and stops allocating.
	const n = 100
	var q Fifo[*int]
	next, want := 0, 0
	push := func() {
		v := next
		q.Unshift(&v)
		next++
	}
	churn := func() {
		p, ok := q.Shift()
		if !ok || *p != want {
			t.Fatalf("Shift() = %v, %t, want %d", p, ok, want)
		}
		want++
		push()
	}
	for range n {
		push()
	}
	for range 10 * n {
		churn()
		checkReleased(t, &q)
	}
	var v int
	if allocs := testing.AllocsPerRun(10*n, func() {
		q.Shift()
		q.Unshift(&v)
	}); allocs != 0 {
		t.Errorf("steady Shift and Unshift allocate %v times, want 0", allocs)
	}
}

func TestFifoDeleteFunc(t *testing.T) {
	var q Fifo[int]
	for i := range 6 {
		q.Unshift(i)
	}
	q.Shift()
	q.DeleteFunc(func(v int) bool { return v%2 == 0 })
	if vs := slices.Collect(q.Values()); !slices.Equal(vs, []int{1, 3, 5}) {
		t.Errorf("after DeleteFunc, queue = %v, want [1 3 5]", vs)
	}
	if q.Len() != 3 {
		t.Errorf("Len() = %d, want 3", q.Len())
	}
}

func TestFifoDeleteOne(t *testing.T) {
	var q Fifo[int]
	for i := range 10 {
		q.Unshift(i)
	}
	q.Shift()
	q.Shift()
	want := []int{2, 3, 4, 5, 6, 7, 8, 9}
	// Front, back, next to each end, the middle, a value not there, and
	// then everything left.
	for _, v := range []int{2, 9, 3, 8, 5, 42, 4, 6, 7} {
		ok := q.DeleteOne(func(x int) bool { return x == v })
		i := slices.Index(want, v)
		if ok != (i >= 0) {
			t.Errorf("DeleteOne(%d) = %t, want %t", v, ok, i >= 0)
		}
		if i >= 0 {
			want = slices.Delete(want, i, i+1)
		}
		if vs := slices.Collect(q.Values()); !slices.Equal(vs, want) {
			t.Fatalf("after DeleteOne(%d), queue = %v, want %v", v, vs, want)
		}
	}
	if q.Len() != 0 || q.DeleteOne(func(int) bool { return true }) {
		t.Errorf("empty queue: Len() = %d, or DeleteOne found a value", q.Len())
	}
	q.Unshift(1)
	if v, ok := q.Shift(); v != 1 || !ok {
		t.Errorf("Shift() after emptying = %d, %t, want 1, true", v, ok)
	}
}

func TestFifoDeleteOneReleases(t *testing.T) {
	var q Fifo[*int]
	ps := make([]*int, 8)
	for i := range ps {
		ps[i] = new(int)
		q.Unshift(ps[i])
	}
	q.Shift()
	for _, i := range []int{1, 7, 4, 2, 6} {
		p := ps[i]
		if !q.DeleteOne(func(x *int) bool { return x == p }) {
			t.Fatalf("DeleteOne(ps[%d]) = false, want true", i)
		}
		checkReleased(t, &q)
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
