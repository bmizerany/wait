# wait

[![Go Reference](https://pkg.go.dev/badge/blake.io/wait.svg)](https://pkg.go.dev/blake.io/wait)

Package `wait` controls access to scarce things like connections, device
handles, worker capacity, and API quota, when you need both a hard limit and
fair service. Callers wait their turn in the order they arrived. Nobody creates
past the limit, and nobody keeps racing for whatever comes back next.

`wait.List` is a pool of the items you add with `Add`. Waiting callers get
items in arrival order. Idle items sit in a LIFO stack, so the next caller gets
the most recently used one, which is the likeliest to still be warm. `MaxWaiters` caps how many callers can wait, and a
context lets a caller give up or wait only until a deadline.

`Take` puts you in line and hands you a `Ticket`. The ticket's `Value` waits
your turn, and its `Release` gives the item back. Releasing twice, or releasing
a ticket that never got in, does nothing.

```go
t := conns.Take(ctx)
defer t.Release()
c, err := t.Value()
if err != nil {
	return err
}
```

A list never creates items. To create connections lazily, keep them warm, or
replace broken ones, add slots that dial for themselves: a slot starts a dial
in the background when it's made and again when its connection breaks, so
whoever takes it next usually finds a connection ready. The package's slot
example shows how.

A `List` holding a single item is a fair lock: whoever holds the item has
the turn. The package's gate example uses two such lists to admit requests
of different sizes, like VMs with different CPU and disk needs, against a
shared budget, strictly in arrival order.

If you don't need a hard limit on creation or first-come service, a buffered
channel is simpler. `sync.Pool` is for reusing temporary allocations; it
doesn't limit how many resources exist, and it doesn't order callers.

API details and examples are in the
[package documentation](https://pkg.go.dev/blake.io/wait).
