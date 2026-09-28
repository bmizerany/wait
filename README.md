# wait

[![Go Reference](https://pkg.go.dev/badge/blake.io/wait.svg)](https://pkg.go.dev/blake.io/wait)

Package `wait` controls access to scarce things like connections, device
handles, worker capacity, and API quota, when you need both a hard limit and
fair service. You set the limit by how many items you add; the list never
creates more. Callers wait their turn in the order they arrived, and nobody
keeps racing for whatever comes back next.

An item that comes back goes straight to the caller that has waited longest,
never to one that arrives a moment later, so no caller is passed over while
items keep coming back.

`wait.List` is a pool of the items you add with `Add`. Waiting callers get
items in arrival order. Idle items sit in a LIFO stack, so the next caller gets
the most recently used one, which is the likeliest to still be warm.
`MaxWaiters` caps how many callers can wait, and a context lets a caller give
up or wait only until a deadline.

`Take` puts you in line and hands you a `Ticket`. The ticket's `Value` waits
your turn, and its `Done` gives the item back. Calling `Done` twice, or on a
ticket that never got in, does nothing. To keep the item instead, or replace a
broken one with `Add`, call `Leave` before the deferred `Done`.

```go
t := conns.Take(ctx)
defer t.Done()
c, err := t.Value()
if err != nil {
	return err
}
```

A list never creates items. To dial ahead or replace a broken connection, add
connections that redial themselves: one dials in the background when it's made
and again when it breaks, so whoever takes it next usually finds it connected.
The package's redial example shows how.

A `List` holding a single item is a fair lock: whoever holds the item has
the turn. The package's demand example uses two such lists to share a link's
connections and bandwidth among downloads of different sizes, strictly in
arrival order, in one short function.

Unlike a buffered channel, a list hands out the most recently returned item
instead of the one idle longest, holds your place in line without blocking so
you can get ready while you wait, and takes an item back only once no matter
how many times you call `Done`. A channel is simpler when none of that matters.
`sync.Pool` is for reusing temporary allocations; it doesn't limit how many
resources exist, and it doesn't order callers.

API details and examples are in the
[package documentation](https://pkg.go.dev/blake.io/wait).
