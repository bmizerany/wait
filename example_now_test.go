package wait_test

import (
	"context"
	"fmt"

	"blake.io/wait"
)

// now is already done, so Take with it takes only what is available at
// once: a ready item, or capacity that fits, and never a place in line.
// Make it once and use it everywhere, instead of a new context per call.
var now = func() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}()

func Example_now() {
	var conns wait.List[string]
	conns.Add("conn-a")

	t := conns.Take(now)
	if c, err := t.Value(); err == nil {
		fmt.Println("took", c)
	}
	if _, err := conns.Take(now).Value(); err != nil {
		fmt.Println("no conn ready:", err)
	}
	t.Release()

	// Output:
	// took conn-a
	// no conn ready: context canceled
}
