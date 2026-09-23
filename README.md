# wait

[![Go Reference](https://pkg.go.dev/badge/blake.io/wait.svg)](https://pkg.go.dev/blake.io/wait)

Package `wait` controls access to scarce things like connections, device
handles, worker capacity, and API quota, when you need both a hard limit and
fair service. Callers wait their turn in the order they arrived. Nobody creates
past the limit, and nobody keeps racing for whatever comes back next.

`wait.List` is a pool. You add items with `Add`, and it can create more lazily,
up to `MaxItems`. Waiting callers get items in arrival order. Idle items sit in
a LIFO stack, so the next caller gets the most recently used one, which is the
likeliest to still be warm. `MaxWaiters` caps how many callers can wait, and a
context lets a caller give up or wait only until a deadline.

`wait.Gate` holds nothing. It lines up requests against capacity you keep track
of yourself. It admits strictly in arrival order, so a large request at the
front can hold up smaller ones behind it.

Both work the same way. `Take` puts you in line and hands you a `Ticket`.
The ticket's `Value` waits your turn, and its `Release` gives back what you
got. Releasing twice, or releasing a ticket that never got in, does nothing.

```go
t := conns.Take(ctx)
defer t.Release()
c, err := t.Value()
if err != nil {
	return err
}
```

If you don't need a hard limit on creation or first-come service, a buffered
channel is simpler. `sync.Pool` is for reusing temporary allocations; it
doesn't limit how many resources exist, and it doesn't order callers.

API details and examples are in the
[package documentation](https://pkg.go.dev/blake.io/wait).
