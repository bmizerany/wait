package wait

// Future holds the result of a reservation made by [List.Reserve]. Its zero
// value is not usable. Wait may be called repeatedly or concurrently; every
// call returns the same result.
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

// Wait blocks until the reservation has completed. If it returns an item,
// the caller owns one checkout and must return it with [List.Put] or retire it
// with [List.Retire].
func (f *Future[T]) Wait() (T, error) {
	<-f.done
	return f.value, f.err
}
