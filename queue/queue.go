// Package queue provides FIFO queues and LIFO stacks.
package queue

import (
	"iter"
	"slices"
)

// A Fifo is a first-in, first-out queue. Its zero value is ready to use.
//
// Front, Shift, and Len take O(1) time, and Unshift takes amortized O(1)
// time. DeleteOne takes time proportional to the distance between the
// value it removes and the nearer end of the queue, so O(n) at worst.
type Fifo[E any] struct {
	// The queue is a[head:]. Shift advances head rather than moving the
	// rest down, and a[:head] is zeroed so that it holds on to nothing.
	a    []E
	head int
}

// Front returns the first value without removing it, or a zero value and
// false if the queue is empty.
func (q *Fifo[E]) Front() (v E, ok bool) {
	var zero E
	if q.head == len(q.a) {
		return zero, false
	}
	return q.a[q.head], true
}

// Unshift adds v to the end of the queue.
func (q *Fifo[E]) Unshift(v E) {
	// When a is full and at least half of it is vacated, move the queue
	// down instead of growing a: each value is moved at most once for
	// every value shifted out, and a stops growing at twice the longest
	// queue.
	if len(q.a) == cap(q.a) && q.head > 0 && q.head >= len(q.a)/2 {
		n := copy(q.a, q.a[q.head:])
		clear(q.a[n:])
		q.a = q.a[:n]
		q.head = 0
	}
	q.a = append(q.a, v)
}

// Shift removes and returns the first value, or a zero value and false if the
// queue is empty.
func (q *Fifo[E]) Shift() (v E, ok bool) {
	var zero E
	if q.head == len(q.a) {
		return zero, false
	}
	v = q.a[q.head]
	q.a[q.head] = zero // release the vacated slot, like Pop
	q.head++
	if q.head == len(q.a) {
		q.a, q.head = q.a[:0], 0
	}
	return v, true
}

// DeleteOne removes a value for which f returns true, if there is one,
// and reports whether it did. It checks values in pairs, one from each
// end, working toward the middle. If f returns true for more than one
// value, which one DeleteOne removes is unspecified.
func (q *Fifo[E]) DeleteOne(f func(E) bool) bool {
	for i, j := q.head, len(q.a)-1; i <= j; i, j = i+1, j-1 {
		if f(q.a[i]) {
			q.deleteAt(i)
			return true
		}
		if f(q.a[j]) {
			q.deleteAt(j)
			return true
		}
	}
	return false
}

// deleteAt removes a[i], moving whichever side of it is shorter.
func (q *Fifo[E]) deleteAt(i int) {
	var zero E
	if i-q.head < len(q.a)-1-i {
		copy(q.a[q.head+1:i+1], q.a[q.head:i])
		q.a[q.head] = zero
		q.head++
	} else {
		copy(q.a[i:], q.a[i+1:])
		q.a[len(q.a)-1] = zero
		q.a = q.a[:len(q.a)-1]
	}
	if q.head == len(q.a) {
		q.a, q.head = q.a[:0], 0
	}
}

// Len returns the number of values in the queue.
func (q *Fifo[E]) Len() int {
	return len(q.a) - q.head
}

// Values returns an iterator over the values in queue order.
func (q *Fifo[E]) Values() iter.Seq[E] {
	return slices.Values(q.a[q.head:])
}

// A Lifo is a last-in, first-out stack. Its zero value is ready to use.
//
// Pop and Len take O(1) time, and Push takes amortized O(1) time.
type Lifo[E any] struct {
	a []E
}

// Push adds v to the top of the stack.
func (q *Lifo[E]) Push(v E) {
	q.a = append(q.a, v)
}

// Pop removes and returns the top value, or a zero value and false if the
// stack is empty.
func (q *Lifo[E]) Pop() (v E, ok bool) {
	var zero E
	if len(q.a) == 0 {
		return zero, false
	}
	v = q.a[len(q.a)-1]
	q.a[len(q.a)-1] = zero
	q.a = q.a[:len(q.a)-1]
	return v, true
}

// Len returns the number of values in the stack.
func (q *Lifo[E]) Len() int {
	return len(q.a)
}
