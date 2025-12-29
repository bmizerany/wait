// Package queue implements FIFO and LIFO queues.
package queue

import "slices"

type Fifo[E any] struct {
	a []E
}

func (q *Fifo[E]) Unshift(v E) {
	q.a = append(q.a, v)
}

func (q *Fifo[E]) Shift() (v E, ok bool) {
	var zero E
	if len(q.a) == 0 {
		return zero, false
	}
	v = q.a[0]
	q.a = append(q.a[:0], q.a[1:]...)
	return v, true
}

func (q *Fifo[E]) DeleteFunc(f func(E) bool) {
	q.a = slices.DeleteFunc(q.a, f)
}

func (q *Fifo[E]) Len() int {
	return len(q.a)
}

type Lifo[E any] struct {
	a []E
}

func (q *Lifo[E]) Push(v E) {
	q.a = append(q.a, v)
}

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

func (q *Lifo[E]) DeleteFunc(f func(E) bool) {
	q.a = slices.DeleteFunc(q.a, f)
}

func (q *Lifo[T]) Len() int {
	return len(q.a)
}
