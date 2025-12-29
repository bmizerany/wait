# wait

[![Go Reference](https://pkg.go.dev/badge/blake.io/wait.svg)](https://pkg.go.dev/blake.io/wait)

A waitlist for pooling reusable resources.

## What

`wait.List` manages a pool of items where waiters are served in FIFO order and item creation is lazy up to a configurable limit.

## Why

Sometimes you need to bound how many of something you create—database connections, file handles, expensive objects. And sometimes fairness matters: you don't want request 10,000 to starve while requests 10,001-11,000 get serviced.

A buffered channel can pool things, but can't guarantee fairness or control creation.
`sync.Pool` reduces allocation overhead but doesn't bound creation or provide ordering.
`wait.List` trades some throughput for predictable latency and bounded resource use.

## When

Use this when:
- Resources are expensive to create
- Fairness matters
- You need a hard cap on instances

Don't use this when:
- A buffered channel is sufficient
- You don't care about fairness
- Maximum throughput is the only goal

## How

```go
pool := &wait.List[*sql.Conn]{
    MaxItems:   10,  // never create more than 10 connections
    MaxWaiters: 100, // reject requests if queue is too long
}

conn, err := pool.Take(ctx, func() *sql.Conn {
    // only called if we haven't hit MaxItems
    return openConnection()
})
if err != nil {
    return err
}
defer pool.Put(conn)

// use conn
```

See [package documentation](https://pkg.go.dev/blake.io/wait) for details.
