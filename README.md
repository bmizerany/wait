# wait

[![Go Reference](https://pkg.go.dev/badge/blake.io/wait.svg)](https://pkg.go.dev/blake.io/wait)

Package `wait` controls access to scarce things like connections, device
handles, worker capacity, and API quota, when you need both a hard limit and
fair service. Callers wait their turn in the order they arrived. Nobody creates
past the limit, and nobody keeps racing for whatever comes back next.

`wait.List` is a pool. You add items with `Add`, and it can create more lazily
with `New`, up to `MaxItems`. Waiting callers get items in arrival order. Idle
items sit in a LIFO stack, so the next caller gets the most recently used one,
which is the likeliest to still be warm. `MaxWaiters` caps how many callers can wait, and a
context lets a caller give up or wait only until a deadline.

`Take` puts you in line and hands you a `Ticket`. The ticket's `Value` waits
your turn, and its `Release` gives the item back. Releasing twice, or releasing
a ticket that never got in, does nothing. If an item is broken, don't release
it: it's yours to close, and you can `Add` a replacement.

```go
t := conns.Take(ctx)
c, err := t.Value()
if err != nil {
	return err
}
if err := use(c); err != nil {
	c.Close() // broken: never released, so it never goes back
	return err
}
t.Release()
```

A `List` holding a single item is a fair lock: whoever holds the item has
the turn. The package's gate example uses two such lists to admit requests
of different sizes, like VMs with different CPU and disk needs, against a
shared budget, strictly in arrival order.

If you don't need a hard limit on creation or first-come service, a buffered
channel is simpler. `sync.Pool` is for reusing temporary allocations; it
doesn't limit how many resources exist, and it doesn't order callers.

API details and examples are in the
[package documentation](https://pkg.go.dev/blake.io/wait).
