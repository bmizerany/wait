package wait

// A Future is the result of a [List.Reserve] call. Its zero value is unusable.
// Wait may be called repeatedly or concurrently; each call returns the same
// result.
type Future[T any] struct {
	done  chan struct{}
	value T
	err   error
}

func newFuture[T any]() *Future[T] {
	return &Future[T]{done: make(chan struct{})}
}

// resolve sets the result exactly once. The caller must serialize calls.
func (f *Future[T]) resolve(v T, err error) {
	f.value = v
	f.err = err
	close(f.done)
}

// Wait returns the reservation result, blocking until it is ready. The caller
// owns any returned item and must return it with [List.Put] or remove it with
// [List.Retire].
func (f *Future[T]) Wait() (T, error) {
	<-f.done
	return f.value, f.err
}
