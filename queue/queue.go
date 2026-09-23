// Package queue provides FIFO queues and LIFO stacks.
package queue

import (
	"iter"
	"slices"
)

// A Fifo is a first-in, first-out queue. Its zero value is ready to use.
type Fifo[E any] struct {
	a []E
}

// Front returns the first value without removing it, or a zero value and
// false if the queue is empty.
func (q *Fifo[E]) Front() (v E, ok bool) {
	var zero E
	if len(q.a) == 0 {
		return zero, false
	}
	return q.a[0], true
}

// Unshift adds v to the end of the queue.
func (q *Fifo[E]) Unshift(v E) {
	q.a = append(q.a, v)
}

// Shift removes and returns the first value, or a zero value and false if the
// queue is empty.
func (q *Fifo[E]) Shift() (v E, ok bool) {
	var zero E
	if len(q.a) == 0 {
		return zero, false
	}
	v = q.a[0]
	copy(q.a, q.a[1:])
	q.a[len(q.a)-1] = zero // release the vacated slot, like Pop
	q.a = q.a[:len(q.a)-1]
	return v, true
}

// DeleteFunc removes each value for which f returns true.
func (q *Fifo[E]) DeleteFunc(f func(E) bool) {
	q.a = slices.DeleteFunc(q.a, f)
}

// Len returns the number of values in the queue.
func (q *Fifo[E]) Len() int {
	return len(q.a)
}

// Values returns an iterator over the values in queue order.
func (q *Fifo[E]) Values() iter.Seq[E] {
	return slices.Values(q.a)
}

// A Lifo is a last-in, first-out stack. Its zero value is ready to use.
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

// DeleteFunc removes each value for which f returns true.
func (q *Lifo[E]) DeleteFunc(f func(E) bool) {
	q.a = slices.DeleteFunc(q.a, f)
}

// Len returns the number of values in the stack.
func (q *Lifo[T]) Len() int {
	return len(q.a)
}
