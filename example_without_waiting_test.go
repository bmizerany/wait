package wait_test

import (
	"context"
	"fmt"

	"blake.io/wait"
)

// withoutWaiting is already done, so Take with it takes only an item that
// is ready at once, and never a place in line. Make it once and use it
// everywhere, instead of a new context per call.
var withoutWaiting = func() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}()

func Example_withoutWaiting() {
	var conns wait.List[string]
	conns.Add("conn-a")

	t := conns.Take(withoutWaiting)
	defer t.Release()
	if c, err := t.Value(); err == nil {
		fmt.Println("took", c)
	}
	// Nothing else is ready, so this Take fails at once and holds nothing
	// to release.
	if _, err := conns.Take(withoutWaiting).Value(); err != nil {
		fmt.Println("no conn ready:", err)
	}

	// Output:
	// took conn-a
	// no conn ready: context canceled
}
