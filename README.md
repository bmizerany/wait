# wait

[![Go Reference](https://pkg.go.dev/badge/blake.io/wait.svg)](https://pkg.go.dev/blake.io/wait)

When many goroutines need the same scarce resource, an ordinary pool can leave
you with two bad choices: create resources without a firm limit, or make
callers compete for returned resources with no useful fairness guarantee.
`wait` lets you set a hard limit and keeps waiting callers in line, so a new
arrival cannot repeatedly jump ahead of work already waiting.

Use it for database connections, API concurrency budgets, device handles, or
any reusable resource that is expensive or unsafe to over-create. Use
`wait.List` when the package should own the pool of resources. Use `wait.Gate`
when your program already owns the resources and needs to decide which request
gets to use limited capacity next.

If you do not need a hard creation limit or FIFO service, a buffered channel is
usually simpler. If you only want to reuse temporary allocations, use
`sync.Pool`.

## Pool resources with `List`

`List` is useful when you need to cap live resources without eagerly creating
them all. For example, a service can reuse up to ten database connections,
while additional requests wait instead of opening an eleventh connection or
crowding out earlier requests. `MaxWaiters` sets a hard cap on pending `Take`
calls and reservations; excess requests return `ErrMaxWaiters` instead of
joining the queue. A request context can also bound how long a caller waits:
cancellation removes a pending `Take` or `Reserve` from the line. Give the
context a deadline when waiting longer than a fixed duration is not useful.

Queued callers receive items in FIFO order. When no callers are waiting,
returned items are kept in a LIFO stack for reuse. Items are created lazily up
to `MaxItems`. Zero limits mean no limit; the zero value is ready to use and
creates zero-valued items if `New` is nil.

Use `Take` to wait for an item when it is needed:

```go
var conns wait.List[*sql.Conn]
conns.MaxItems = 10
conns.New = openConnection // openConnection returns *sql.Conn

conn, err := conns.Take(ctx)
if err != nil {
	return err
}
defer conns.Put(conn)

// use conn
```

`List.New` has no error result, so handle fallible creation outside `List` or
include the failure in the item type. Every item returned by `Take` must be
returned with `Put` or removed from the pool with `Retire`.

Use `Reserve` to take a place in line before doing other work:

```go
future, err := conns.Reserve(ctx)
if err != nil {
	return err
}

prepareRequest()

conn, err := future.Wait()
if err != nil {
	return err
}
defer conns.Put(conn)

// use conn
```

Canceling the reservation context removes a pending reservation, even if
`Wait` has not been called. If the item was assigned first, the future holds a
checkout; call `Wait` even after cancellation and return or retire any item it
returns. `Future.Wait` may be called repeatedly or concurrently and returns
the same result each time.

`Close` wakes pending calls and rejects later returns. Ready items can still
be drained after close with `Take`, `Reserve`, or `TryTake`. Close the list
when its owner no longer accepts new work; checked-out items remain the
caller's responsibility.

## Limit access with `Gate`

`Gate` is useful when resources do not belong in a pool, but concurrent work
still needs a budget. For example, requests may consume different amounts of
API quota, memory, or worker capacity. `Gate` lets the application decide
whether the next request fits, and makes later requests wait their turn. It
holds no items; the caller tracks available capacity in `Fill` and `Refill`.
Admission is strictly in arrival order, so a large request at the front can
hold smaller requests behind it until enough capacity is available.

```go
var available = 10
g := wait.Gate[int]{
	Fill: func(n int) bool {
		if n > available {
			return false
		}
		available -= n
		return true
	},
	Refill: func(n int) { available += n },
}

if err := g.Wait(ctx, 3); err != nil {
	return err
}
defer g.Put(3)
```

`Fill` and `Refill` run while the gate is locked. They must not block or call
back into that gate. `Fill` changes the caller's accounting only when it
returns true; `Refill` returns capacity when a caller releases its demand.
`Gate` has no waiter-count limit, so pass a context with a deadline to `Wait`
when queued demands should stop waiting after a fixed duration. Use `TryWait`
when a demand should fail immediately instead of queueing.

## Choosing a primitive

Use `List` when the package should store, create, hand off, and count reusable
items. Use `Gate` when the caller owns the resources and needs FIFO admission
to its own capacity accounting. A buffered channel can be simpler when FIFO
fairness and a creation limit do not matter; `sync.Pool` is for temporary
allocation reuse and does not impose either limit or ordering.

See the [package documentation](https://pkg.go.dev/blake.io/wait) for the
complete API contracts.
