//go:build !race

// The fixture of pool's performance contract (design/sdk.yaml, budgets):
// it sets one call up and returns it, and the gen_perf_test.go kit gen
// writes beside this file counts its allocations against its budget. Race
// off.
package pool_test

import (
	"context"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/concur/pool"
)

// perfErr keeps each call's result alive.
var perfErr error

// perfSubmit is Submit's fixture: a task handed to a running pool whose
// queue always has room — a thousand calls behind it at most —, the task
// built once, as a caller's closure is. The pool lives for the test.
func perfSubmit(tb testing.TB) func() {
	p := pool.NewPool(context.Background(), pool.Config{Workers: 2, Queue: 1 << 16})
	tb.Cleanup(func() { _ = p.Drain(context.Background()) })
	ctx := context.Background()
	task := func(context.Context) error { return nil }
	return func() {
		perfErr = p.Submit(ctx, task)
	}
}
