# wait

[![Go Reference](https://pkg.go.dev/badge/blake.io/wait.svg)](https://pkg.go.dev/blake.io/wait)

Package `wait` coordinates access to scarce resources when both a hard limit
and fair service matter. It is useful for connection pools, device handles,
worker capacity, and API quotas: callers wait in arrival order instead of
creating beyond a limit or repeatedly racing for the next available resource.

`wait.List` pools reusable items supplied with `Put` and can create more
lazily up to `MaxItems`. It serves queued callers in FIFO order. `MaxWaiters`
caps the queue; contexts let callers cancel pending work or bound their wait
with a deadline. `wait.Gate` owns no resources and orders admission against
capacity tracked by the caller. Its strict arrival order can leave smaller
requests waiting behind a larger one.

Use a buffered channel when a hard creation limit and FIFO service are not
needed. Use `sync.Pool` for temporary allocation reuse; it does not bound
resource creation or order callers.

See the [package documentation](https://pkg.go.dev/blake.io/wait) for API
details and examples.
