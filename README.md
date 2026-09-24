# wait

[![Go Reference](https://pkg.go.dev/badge/blake.io/wait.svg)](https://pkg.go.dev/blake.io/wait)

Package `wait` controls access to scarce things like connections, device
handles, worker capacity, and API quota, when you need both a hard limit and
fair service. Callers wait their turn in the order they arrived. Nobody creates
past the limit, and nobody keeps racing for whatever comes back next.

An item that comes back goes straight to the caller that has waited longest,
never to one that arrives a moment later, so no caller is passed over while
items keep coming back.

`wait.List` is a pool of the items you add with `Add`. Waiting callers get
items in arrival order. Idle items sit in a LIFO stack, so the next caller gets
the most recently used one, which is the likeliest to still be warm.
`MaxWaiters` caps how many callers can wait, and a context lets a caller give
up or wait only until a deadline.

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
the turn. The package's VM example uses two such lists to share a host's CPUs
and memory among VMs of different sizes, strictly in arrival order, in about
thirty lines.

Unlike a buffered channel, a list hands out the most recently returned item
instead of the one idle longest, holds your place in line without blocking so
you can get ready while you wait, and takes an item back only once no matter
how many times you release it. A channel is simpler when none of that matters.
`sync.Pool` is for reusing temporary allocations; it doesn't limit how many
resources exist, and it doesn't order callers.

API details and examples are in the
[package documentation](https://pkg.go.dev/blake.io/wait).
